package sites

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"goveto-edge/internal/configseal"
	"goveto-edge/internal/edgeprotocol"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/model"
	"goveto-edge/internal/storage/gen/query"
)

// sealedGovernanceJSON marshals an origin governance policy for storage with
// its mTLS client key sealed to the cluster and origin pool. Policies without
// an mTLS key marshal unchanged; a policy that already carries an envelope is
// rejected so double sealing cannot hide programming errors.
func sealedGovernanceJSON(sealer *configseal.Sealer, clusterID, poolID string, policy edgeprotocol.OriginPolicyConfig) ([]byte, error) {
	if sealer.HasOriginPolicySecrets(&policy) {
		if sealer == nil {
			return nil, fmt.Errorf("seal origin governance: %w", configseal.ErrSealerUnavailable)
		}
		if err := sealer.SealOriginPolicySecrets(clusterID, poolID, &policy); err != nil {
			return nil, fmt.Errorf("seal origin governance: %w", err)
		}
	}
	encoded, err := json.Marshal(policy)
	if err != nil {
		return nil, fmt.Errorf("encode origin governance: %w", err)
	}
	return encoded, nil
}

// unmarshalGovernancePolicy parses a stored governance policy and unseals its
// mTLS client key. A nil sealer skips unsealing; callers that need plaintext
// credentials must provide one.
func unmarshalGovernancePolicy(sealer *configseal.Sealer, clusterID, poolID string, governance json.RawMessage) (edgeprotocol.OriginPolicyConfig, error) {
	policy, err := edgeprotocol.ParseOriginPolicy(governance)
	if err != nil {
		return edgeprotocol.OriginPolicyConfig{}, err
	}
	if sealer == nil || !sealer.SealedOriginPolicy(&policy) {
		return policy, nil
	}
	if err = sealer.UnsealOriginPolicySecrets(clusterID, poolID, &policy); err != nil {
		return edgeprotocol.OriginPolicyConfig{}, fmt.Errorf("unseal origin governance: %w", err)
	}
	return policy, nil
}

// GovernanceRewrapFailure identifies an origin pool whose governance could not
// be migrated.
type GovernanceRewrapFailure struct {
	PoolID    string
	ClusterID string
	Err       error
}

// GovernanceRewrapResult reports corrupt or unreadable governance rows
// skipped during startup rewrap so one bad row cannot take the control plane
// down.
type GovernanceRewrapResult struct {
	Skipped []GovernanceRewrapFailure
}

// RewrapOriginGovernanceSecrets reseals origin pool governance onto the
// current primary key, including plaintext keys written before sealing
// existed. It is idempotent and safe to re-run on every startup.
func RewrapOriginGovernanceSecrets(ctx context.Context, db *client.Client, sealer *configseal.Sealer) (GovernanceRewrapResult, error) {
	if sealer == nil {
		return GovernanceRewrapResult{}, nil
	}
	pools, err := db.OriginPool.Query().Do(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return GovernanceRewrapResult{}, nil
		}
		return GovernanceRewrapResult{}, err
	}
	return rewrapOriginGovernanceRows(ctx, pools, sealer.RewrapOriginPolicySecrets, func(id string, encoded []byte) error {
		_, updateErr := db.OriginPool.Update().
			Where(query.OriginPool.Id.Equals(id)).
			Set(query.OriginPool.Governance.Set(encoded)).
			Do(ctx)
		return updateErr
	})
}

func rewrapOriginGovernanceRows(
	ctx context.Context,
	pools []model.OriginPool,
	rewrap func(clusterID, poolID string, policy *edgeprotocol.OriginPolicyConfig) (bool, error),
	persist func(id string, encoded []byte) error,
) (GovernanceRewrapResult, error) {
	result := GovernanceRewrapResult{}
	skipRemaining := func(from int, err error) {
		for index := from; index < len(pools); index++ {
			pool := &pools[index]
			result.Skipped = append(result.Skipped, GovernanceRewrapFailure{
				PoolID: pool.Id, ClusterID: pool.ClusterId, Err: err,
			})
		}
	}
	for index := range pools {
		if err := ctx.Err(); err != nil {
			skipRemaining(index, err)
			return result, nil
		}
		pool := &pools[index]
		skip := func(err error) {
			result.Skipped = append(result.Skipped, GovernanceRewrapFailure{
				PoolID: pool.Id, ClusterID: pool.ClusterId, Err: err,
			})
		}
		policy, parseErr := edgeprotocol.ParseOriginPolicy(pool.Governance)
		if parseErr != nil {
			skip(parseErr)
			continue
		}
		changed, rewrapErr := rewrap(pool.ClusterId, pool.Id, &policy)
		if rewrapErr != nil {
			skip(rewrapErr)
			continue
		}
		if !changed {
			continue
		}
		encoded, marshalErr := json.Marshal(policy)
		if marshalErr != nil {
			skip(marshalErr)
			continue
		}
		if err := persist(pool.Id, encoded); err != nil {
			if ctx.Err() != nil {
				skipRemaining(index, ctx.Err())
				return result, nil
			}
			return result, fmt.Errorf("persist rewrapped origin governance %s: %w", pool.Id, err)
		}
		slog.Info("resealed origin governance", "origin_pool_id", pool.Id, "cluster_id", pool.ClusterId)
	}
	return result, nil
}
