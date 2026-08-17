package dnssync

import (
	"math"
	"sort"
	"time"

	"goveto-edge/internal/storage/gen/model"
)

// NodeCandidate is a DNS-eligible node together with the signals the
// scheduler needs to decide whether it should currently be published.
type NodeCandidate struct {
	Node      model.Node
	Addresses []model.NodeAddress
	// Lines are the DNS lines assigned to the node. A nil slice (or a slice
	// containing only nil) means the default line only.
	Lines []*model.DNSLine
}

// SchedulerPolicy bounds how aggressively DNS records may follow node state.
// The defaults favor availability: nodes enter DNS conservatively and leave
// in bounded steps, so a monitoring storm cannot drain traffic at once.
type SchedulerPolicy struct {
	// Placement selects which candidates are published. ALL keeps the
	// historical behavior of publishing every eligible node.
	// PRIMARY_BACKUP publishes the lowest-priority-value tier first and only
	// pulls in further tiers until MinPublished nodes are covered.
	Placement model.DNSPlacement
	// MinPublished is the number of nodes PRIMARY_BACKUP aims to publish.
	MinPublished int
	// MinHealthyTime is how long a node must have been continuously online
	// before it may be newly added to DNS. Nodes already published are not
	// affected. Nil falls back to the default; an explicit zero duration
	// disables the guard. Nodes without a known online_since (for example
	// created by an older control plane) are admitted directly.
	MinHealthyTime *time.Duration
	// MaxRemovalRatio caps the share of currently published nodes that a
	// single reconciliation may remove. The cap covers every published node
	// that is about to leave DNS, whether it was evicted by placement or left
	// the eligible set entirely (offline beyond the grace period, disabled,
	// or revoked). Excess removals are deferred to the next pass. The cap
	// does not apply when no candidate remains, because keeping records that
	// point at dead nodes is never safer.
	MaxRemovalRatio float64
}

// DefaultSchedulerPolicy returns the policy used when no overrides are set.
func DefaultSchedulerPolicy() SchedulerPolicy {
	return SchedulerPolicy{
		Placement:       model.DNSPlacementALL,
		MinPublished:    2,
		MinHealthyTime:  durationPointer(2 * time.Minute),
		MaxRemovalRatio: 0.34,
	}
}

// durationPointer returns d as a pointer so policy fields can distinguish an
// explicit zero from an unset value.
func durationPointer(d time.Duration) *time.Duration {
	return &d
}

// WithDefaults fills unset policy fields with the defaults.
func (p SchedulerPolicy) WithDefaults() SchedulerPolicy {
	defaults := DefaultSchedulerPolicy()
	if p.Placement == "" {
		p.Placement = defaults.Placement
	}
	if p.MinPublished <= 0 {
		p.MinPublished = defaults.MinPublished
	}
	if p.MinHealthyTime == nil {
		p.MinHealthyTime = defaults.MinHealthyTime
	}
	if p.MaxRemovalRatio <= 0 || p.MaxRemovalRatio > 1 {
		p.MaxRemovalRatio = defaults.MaxRemovalRatio
	}
	return p
}

// Select returns the candidates that should be published given the node ids
// currently present in DNS. It applies, in order:
//
//  1. admission hysteresis: unpublished nodes must have been online for
//     MinHealthyTime before they are added (already-published nodes are
//     sticky and never held back);
//  2. placement: ALL, or PRIMARY_BACKUP tier expansion up to MinPublished.
//
// Removal pacing is deliberately not done here: nodes that left the eligible
// set are invisible to Select, so the service layer enforces
// MaxRemovalRatio where the full published set and node states are known.
// The returned slice keeps candidate order and is deterministic.
func Select(candidates []NodeCandidate, published map[string]bool, policy SchedulerPolicy, now time.Time) []NodeCandidate {
	policy = policy.WithDefaults()

	admitted := make([]NodeCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if published[candidate.Node.Id] || healthyLongEnough(candidate.Node, policy.MinHealthyTime, now) {
			admitted = append(admitted, candidate)
		}
	}
	return selectByPlacement(admitted, policy)
}

// healthyLongEnough reports whether the node has been online for at least
// minHealthy. A nil or non-positive minHealthy disables the guard, and a
// missing onlineSince is treated as healthy to avoid locking nodes out of
// DNS after a control-plane upgrade.
func healthyLongEnough(node model.Node, minHealthy *time.Duration, now time.Time) bool {
	if minHealthy == nil || *minHealthy <= 0 {
		return true
	}
	if node.OnlineSince == nil {
		return true
	}
	return !now.Before(node.OnlineSince.Add(*minHealthy))
}

// selectByPlacement applies the placement strategy to admitted candidates.
func selectByPlacement(admitted []NodeCandidate, policy SchedulerPolicy) []NodeCandidate {
	if policy.Placement != model.DNSPlacementPRIMARY_BACKUP {
		return append([]NodeCandidate(nil), admitted...)
	}
	selected := make([]NodeCandidate, 0, len(admitted))
	covered := 0
	usedTiers := map[int]bool{}
	for {
		best := -1
		for _, candidate := range admitted {
			tier := candidate.Node.DnsPriority
			if !usedTiers[tier] && (best == -1 || tier < best) {
				best = tier
			}
		}
		if best == -1 {
			break
		}
		usedTiers[best] = true
		for _, candidate := range admitted {
			if candidate.Node.DnsPriority == best {
				selected = append(selected, candidate)
				covered++
			}
		}
		if covered >= policy.MinPublished {
			break
		}
	}
	return selected
}

// allowedRemovals returns how many of the published nodes one reconciliation
// pass may drop. It is always at least one so deferred removals still make
// progress on subsequent passes instead of being deferred forever.
func allowedRemovals(published int, ratio float64) int {
	allowed := int(math.Floor(ratio * float64(published)))
	if allowed < 1 {
		allowed = 1
	}
	return allowed
}

// deferredRemovals picks which dropped nodes stay published for one more
// pass. Candidates are ordered newest-first by UpdatedAt: the freshest
// failures are the most likely transient and are kept, while older ones are
// removed first, which also guarantees deferral progresses over time.
func deferredRemovals(dropped []model.Node, keep int) []model.Node {
	if keep <= 0 {
		return nil
	}
	if keep >= len(dropped) {
		return append([]model.Node(nil), dropped...)
	}
	ordered := append([]model.Node(nil), dropped...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].UpdatedAt.After(ordered[j].UpdatedAt)
	})
	return ordered[:keep]
}
