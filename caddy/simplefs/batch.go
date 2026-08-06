package simplefs

import (
	"container/heap"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync/atomic"
	"syscall"
	"time"

	bolt "go.etcd.io/bbolt"
	core "goveto-edge/internal/cachecore"
)

const (
	writeBatchDelay      = 2 * time.Millisecond
	writeBatchMaxObjects = 64
	writeBatchMaxBytes   = 64 << 20
	writeQueueMaxObjects = 256
	writeQueueMaxBytes   = 256 << 20
)

type pendingWrite struct {
	baseKey        string
	variedKey      string
	temporaryPath  string
	finalPath      string
	compressedSize uint64
	physicalSize   uint64
	originalSize   uint64
	checksum       [32]byte
	modifiedAt     int64
	groups         []string
	variedHeaders  http.Header
	etag           string
	duration       time.Duration
	realKey        string
	done           chan error
}

type expirationEntry struct {
	at         time.Time
	key        string
	generation uint64
	index      int
}

type expirationHeap []*expirationEntry

func (h expirationHeap) Len() int           { return len(h) }
func (h expirationHeap) Less(i, j int) bool { return h[i].at.Before(h[j].at) }
func (h expirationHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index = i
	h[j].index = j
}
func (h *expirationHeap) Push(value any) {
	entry := value.(*expirationEntry)
	entry.index = len(*h)
	*h = append(*h, entry)
}
func (h *expirationHeap) Pop() any {
	old := *h
	index := len(old) - 1
	last := old[index]
	old[index] = nil
	*h = old[:index]
	last.index = -1
	return last
}

type stagedItem struct {
	item    cacheItem
	present bool
}

type batchState struct {
	items           map[string]stagedItem
	groups          map[string]map[string]struct{}
	variantMappings map[string]map[string]struct{}
	itemGroups      map[string]map[string]struct{}
	used            uint64
	physical        uint64
	obsoleteFiles   map[string]struct{}
	evicted         uint64
	poppedExpiry    []*expirationEntry
}

func newBatchState(p *provider) *batchState {
	return &batchState{
		items:           make(map[string]stagedItem),
		groups:          make(map[string]map[string]struct{}),
		variantMappings: make(map[string]map[string]struct{}),
		itemGroups:      make(map[string]map[string]struct{}),
		used:            p.cacheUsed.Load(),
		physical:        p.physicalUsed.Load(),
		obsoleteFiles:   make(map[string]struct{}),
	}
}

func cloneSet(source map[string]struct{}) map[string]struct{} {
	result := make(map[string]struct{}, len(source))
	for key := range source {
		result[key] = struct{}{}
	}
	return result
}

func (s *batchState) getItem(p *provider, key string) (cacheItem, bool) {
	if staged, ok := s.items[key]; ok {
		return staged.item, staged.present
	}
	item, ok := p.items[key]
	return item, ok
}

func (s *batchState) groupsForItem(p *provider, key string) map[string]struct{} {
	if groups, ok := s.itemGroups[key]; ok {
		return groups
	}
	groups := cloneSet(p.itemGroups[key])
	s.itemGroups[key] = groups
	return groups
}

func (s *batchState) keysForGroup(p *provider, group string) map[string]struct{} {
	if keys, ok := s.groups[group]; ok {
		return keys
	}
	keys := cloneSet(p.groups[group])
	s.groups[group] = keys
	return keys
}

func (s *batchState) mappingsForVariant(p *provider, key string) map[string]struct{} {
	if mappings, ok := s.variantMappings[key]; ok {
		return mappings
	}
	mappings := cloneSet(p.variantMappings[key])
	s.variantMappings[key] = mappings
	return mappings
}

func (s *batchState) removeItemGroups(p *provider, key string) {
	groups := s.groupsForItem(p, key)
	for group := range groups {
		delete(s.keysForGroup(p, group), key)
	}
	clear(groups)
}

func (s *batchState) addItemGroup(p *provider, key, group string) {
	s.keysForGroup(p, group)[key] = struct{}{}
	s.groupsForItem(p, key)[group] = struct{}{}
}

func mappingVariants(item cacheItem) map[string]struct{} {
	result := map[string]struct{}{}
	if item.file {
		return result
	}
	mapping, err := core.DecodeMapping(item.value)
	if err != nil || mapping.GetMapping() == nil {
		return result
	}
	for key := range mapping.Mapping {
		result[key] = struct{}{}
	}
	return result
}

func (s *batchState) stageMapping(p *provider, key string, item *cacheItem) {
	if old, ok := s.getItem(p, key); ok {
		s.used -= min(s.used, old.accountedSize)
		for variant := range mappingVariants(old) {
			delete(s.mappingsForVariant(p, variant), key)
		}
	}
	if item == nil {
		s.items[key] = stagedItem{}
		return
	}
	for variant := range mappingVariants(*item) {
		s.mappingsForVariant(p, variant)[key] = struct{}{}
	}
	item.accountedSize = accountedItemSize(key, *item)
	s.used += item.accountedSize
	s.items[key] = stagedItem{item: *item, present: true}
}

func (s *batchState) removeVariantFromMapping(p *provider, mappingKey, variedKey string) {
	item, ok := s.getItem(p, mappingKey)
	if !ok {
		return
	}
	mapping, err := core.DecodeMapping(item.value)
	if err != nil || mapping.GetMapping() == nil {
		s.stageMapping(p, mappingKey, nil)
		return
	}
	if _, ok = mapping.Mapping[variedKey]; !ok {
		return
	}
	delete(mapping.Mapping, variedKey)
	if len(mapping.Mapping) == 0 {
		s.removeItemGroups(p, mappingKey)
		s.stageMapping(p, mappingKey, nil)
		return
	}
	encoded, err := core.EncodeMapping(mapping)
	if err != nil {
		s.stageMapping(p, mappingKey, nil)
		return
	}
	p.nextVersion++
	item.value = encoded
	item.generation = p.nextVersion
	s.stageMapping(p, mappingKey, &item)
}

func (s *batchState) deleteItem(p *provider, key string) {
	item, ok := s.getItem(p, key)
	if !ok {
		return
	}
	s.removeItemGroups(p, key)
	s.used -= min(s.used, item.accountedSize)
	s.physical -= min(s.physical, item.physicalSize)
	if item.file {
		s.obsoleteFiles[string(item.value)] = struct{}{}
		mappings := cloneSet(s.mappingsForVariant(p, key))
		for mappingKey := range mappings {
			s.removeVariantFromMapping(p, mappingKey, key)
		}
		s.items[key] = stagedItem{}
		return
	}
	s.stageMapping(p, key, nil)
}

func (s *batchState) stageExpired(p *provider, now time.Time) {
	for p.expirations.Len() > 0 && !p.expirations[0].at.After(now) {
		entry := heap.Pop(&p.expirations).(*expirationEntry)
		s.poppedExpiry = append(s.poppedExpiry, entry)
		item, ok := p.items[entry.key]
		if !ok || item.generation != entry.generation || item.expiresAt.After(now) {
			continue
		}
		s.deleteItem(p, entry.key)
	}
}

func (s *batchState) restoreExpiry(p *provider) {
	for _, entry := range s.poppedExpiry {
		if item, ok := p.items[entry.key]; ok && item.generation == entry.generation {
			heap.Push(&p.expirations, entry)
		}
	}
}

func (p *provider) enqueueWrite(write *pendingWrite) error {
	p.batchMu.Lock()
	if len(p.pending) >= writeQueueMaxObjects || p.pendingBytes+write.compressedSize > writeQueueMaxBytes {
		p.queueRejections.Add(1)
		p.rejections.Add(1)
		p.batchMu.Unlock()
		return ErrWriteQueueFull
	}
	p.pending = append(p.pending, write)
	p.pendingBytes += write.compressedSize
	p.queueDepth.Store(uint64(len(p.pending)))
	p.queueBytes.Store(p.pendingBytes)
	updateAtomicMax(&p.queueDepthMax, uint64(len(p.pending)))
	updateAtomicMax(&p.queueBytesMax, p.pendingBytes)
	if !p.flushing {
		p.flushing = true
		go p.flushLoop()
	}
	if len(p.pending) >= writeBatchMaxObjects || p.pendingBytes >= writeBatchMaxBytes {
		select {
		case p.batchWake <- struct{}{}:
		default:
		}
	}
	p.batchMu.Unlock()
	return <-write.done
}

func updateAtomicMax(target *atomic.Uint64, value uint64) {
	for current := target.Load(); value > current && !target.CompareAndSwap(current, value); current = target.Load() {
	}
}

func (p *provider) flushLoop() {
	for {
		p.batchMu.Lock()
		immediate := len(p.pending) >= writeBatchMaxObjects || p.pendingBytes >= writeBatchMaxBytes
		p.batchMu.Unlock()
		if !immediate {
			timer := time.NewTimer(writeBatchDelay)
			select {
			case <-timer.C:
			case <-p.batchWake:
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
			}
		}
		batch := p.takeBatch()
		if len(batch) == 0 {
			return
		}
		started := time.Now()
		results := p.flushBatch(batch)
		p.commitNanos.Add(uint64(time.Since(started)))
		p.writeBatches.Add(1)
		p.batchMu.Lock()
		finished := len(p.pending) == 0
		if finished {
			p.flushing = false
			p.batchCond.Broadcast()
		}
		p.batchMu.Unlock()
		for index, write := range batch {
			write.done <- results[index]
		}
		if finished {
			return
		}
	}
}

func (p *provider) takeBatch() []*pendingWrite {
	p.batchMu.Lock()
	defer p.batchMu.Unlock()
	if len(p.pending) == 0 {
		p.flushing = false
		p.batchCond.Broadcast()
		return nil
	}
	count := 0
	var bytes uint64
	for count < len(p.pending) && count < writeBatchMaxObjects {
		next := p.pending[count].compressedSize
		if count > 0 && bytes+next > writeBatchMaxBytes {
			break
		}
		bytes += next
		count++
	}
	batch := append([]*pendingWrite(nil), p.pending[:count]...)
	p.pending = p.pending[count:]
	p.pendingBytes -= bytes
	p.queueDepth.Store(uint64(len(p.pending)))
	p.queueBytes.Store(p.pendingBytes)
	p.inflightWrites.Store(uint64(len(batch)))
	return batch
}

func (p *provider) drain() {
	p.batchMu.Lock()
	for p.flushing {
		p.batchCond.Wait()
	}
	p.batchMu.Unlock()
}

func (p *provider) flushBatch(batch []*pendingWrite) []error {
	results := make([]error, len(batch))
	p.capacityMu.Lock()
	defer p.capacityMu.Unlock()
	p.mu.Lock()
	defer p.mu.Unlock()
	defer p.inflightWrites.Store(0)

	state := newBatchState(p)
	state.stageExpired(p, time.Now())
	transient := uint64(0)
	for _, write := range batch {
		if write.physicalSize == 0 {
			write.physicalSize = physicalFileSize(write.temporaryPath, write.compressedSize)
		}
		transient += write.physicalSize
	}
	budget, err := p.capacityAvailable(p.limits, transient)
	if err != nil {
		state.restoreExpiry(p)
		for index := range results {
			results[index] = err
		}
		return results
	}

	protected := make(map[string]struct{}, len(batch))
	for index, write := range batch {
		now := time.Now()
		mappingKey := core.MappingKeyPrefix + write.baseKey
		previous, _ := state.getItem(p, mappingKey)
		mapping, mapErr := core.MappingUpdater(write.variedKey, previous.value, now, now.Add(write.duration), now.Add(write.duration+p.stale), write.variedHeaders, write.etag, write.realKey)
		if mapErr != nil {
			results[index] = mapErr
			continue
		}
		old, oldExists := state.getItem(p, write.variedKey)
		oldAccounted := uint64(0)
		oldPhysical := uint64(0)
		if oldExists && old.file {
			oldAccounted = old.accountedSize
			oldPhysical = old.physicalSize
		}
		p.nextVersion++
		body := cacheItem{value: []byte(write.finalPath), file: true, expiresAt: now.Add(write.duration + p.stale), lastAccess: now, generation: p.nextVersion, compressedSize: write.compressedSize, physicalSize: write.physicalSize, originalSize: write.originalSize, checksum: write.checksum, modifiedAt: write.modifiedAt}
		body.accountedSize = accountedItemSize(write.variedKey, body)
		mappingItem := cacheItem{value: mapping, expiresAt: now.Add(write.duration + p.stale), lastAccess: now}
		mappingItem.accountedSize = accountedItemSize(mappingKey, mappingItem)
		oldMapping, _ := state.getItem(p, mappingKey)
		neededAccounted := state.used - min(state.used, oldAccounted) - min(state.used-min(state.used, oldAccounted), oldMapping.accountedSize) + body.accountedSize + mappingItem.accountedSize
		neededPhysical := state.physical - min(state.physical, oldPhysical) + body.physicalSize
		if body.accountedSize+mappingItem.accountedSize > budget.accountedAvailable || body.physicalSize > budget.physicalAvailable {
			results[index] = ErrCapacity
			p.rejections.Add(1)
			continue
		}
		protectedForWrite := cloneSet(protected)
		protectedForWrite[write.variedKey] = struct{}{}
		candidates, enough := state.evictionCandidates(p, protectedForWrite, neededAccounted, budget.accountedAvailable, neededPhysical, budget.physicalAvailable)
		if !enough {
			results[index] = ErrCapacity
			p.rejections.Add(1)
			continue
		}
		for _, key := range candidates {
			state.deleteItem(p, key)
			state.evicted++
		}

		if oldExists {
			state.removeItemGroups(p, write.variedKey)
			if old.file {
				state.used -= min(state.used, old.accountedSize)
				state.physical -= min(state.physical, old.physicalSize)
				if string(old.value) != write.finalPath {
					state.obsoleteFiles[string(old.value)] = struct{}{}
				}
			}
		}
		state.used += body.accountedSize
		state.physical += body.physicalSize
		state.items[write.variedKey] = stagedItem{item: body, present: true}
		protected[write.variedKey] = struct{}{}

		p.nextVersion++
		mappingItem.generation = p.nextVersion
		state.stageMapping(p, mappingKey, &mappingItem)
		for _, group := range write.groups {
			state.addItemGroup(p, write.variedKey, group)
			state.addItemGroup(p, mappingKey, group)
		}
	}

	accepted := make([]*pendingWrite, 0, len(batch))
	acceptedIndexes := make([]int, 0, len(batch))
	for index, write := range batch {
		if results[index] == nil {
			accepted = append(accepted, write)
			acceptedIndexes = append(acceptedIndexes, index)
		}
	}
	if len(accepted) == 0 {
		state.restoreExpiry(p)
		return results
	}
	installed, installErr := installBatchFiles(p.path, accepted)
	if installErr == nil {
		for _, write := range accepted {
			staged := state.items[write.variedKey]
			staged.item.modifiedAt = write.modifiedAt
			state.items[write.variedKey] = staged
		}
		installErr = p.persistBatchLocked(state)
	}
	if installErr != nil {
		state.restoreExpiry(p)
		for path := range installed {
			_ = os.Remove(path)
		}
		for _, index := range acceptedIndexes {
			results[index] = installErr
		}
		p.recordRejectedWrite(installErr)
		return results
	}

	p.applyBatchLocked(state)
	p.evictions.Add(state.evicted)
	p.objectsCommitted.Add(uint64(len(accepted)))
	liveFiles := make(map[string]struct{}, len(state.items))
	for _, staged := range state.items {
		if staged.present && staged.item.file {
			liveFiles[string(staged.item.value)] = struct{}{}
		}
	}
	for path := range state.obsoleteFiles {
		if _, stillUsed := liveFiles[path]; stillUsed {
			continue
		}
		_ = os.Remove(path)
	}
	return results
}

func (s *batchState) evictionCandidates(p *provider, protected map[string]struct{}, neededAccounted, accountedAvailable, neededPhysical, physicalAvailable uint64) ([]string, bool) {
	if neededAccounted <= accountedAvailable && neededPhysical <= physicalAvailable {
		return nil, true
	}
	requiredAccounted := neededAccounted - min(neededAccounted, accountedAvailable)
	requiredPhysical := neededPhysical - min(neededPhysical, physicalAvailable)
	var freedAccounted uint64
	var freedPhysical uint64
	candidates := make([]string, 0)
	for element := p.lru.Front(); element != nil; element = element.Next() {
		key := element.Value.(string)
		if _, keep := protected[key]; keep {
			continue
		}
		item, ok := s.getItem(p, key)
		if !ok || !item.file {
			continue
		}
		candidates = append(candidates, key)
		freedAccounted += item.accountedSize
		freedPhysical += item.physicalSize
		if freedAccounted >= requiredAccounted && freedPhysical >= requiredPhysical {
			return candidates, true
		}
	}
	return nil, false
}

func installBatchFiles(directory string, writes []*pendingWrite) (map[string]struct{}, error) {
	installed := map[string]struct{}{}
	changed := false
	for _, write := range writes {
		if info, err := os.Stat(write.finalPath); err == nil && uint64(info.Size()) == write.compressedSize {
			if valid, verifyErr := fileChecksumMatches(write.finalPath, write.checksum); verifyErr == nil && valid {
				_ = os.Remove(write.temporaryPath)
				write.modifiedAt = info.ModTime().UnixNano()
				continue
			}
		}
		if err := os.Rename(write.temporaryPath, write.finalPath); err != nil {
			if errors.Is(err, syscall.ENOSPC) {
				return installed, ErrCapacity
			}
			return installed, err
		}
		info, err := os.Stat(write.finalPath)
		if err != nil {
			return installed, err
		}
		write.modifiedAt = info.ModTime().UnixNano()
		installed[write.finalPath] = struct{}{}
		changed = true
	}
	if !changed {
		return installed, nil
	}
	dir, err := os.Open(directory)
	if err != nil {
		return installed, err
	}
	err = dir.Sync()
	closeErr := dir.Close()
	return installed, errors.Join(err, closeErr)
}

func fileChecksumMatches(path string, expected [32]byte) (bool, error) {
	actual, err := checksumFile(path)
	return actual == expected, err
}

func checksumFile(path string) ([sha256.Size]byte, error) {
	var result [sha256.Size]byte
	file, err := os.Open(path)
	if err != nil {
		return result, err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err = io.Copy(hash, file); err != nil {
		return result, err
	}
	copy(result[:], hash.Sum(nil))
	return result, nil
}

func (p *provider) persistBatchLocked(state *batchState) error {
	if p.index == nil {
		return errors.New("cache metadata database is not open")
	}
	p.indexWrites.Add(1)
	return p.index.Update(func(tx *bolt.Tx) error {
		items := tx.Bucket(indexItemsBucket)
		groups := tx.Bucket(indexGroupsBucket)
		meta := tx.Bucket(indexMetaBucket)
		dirtyItems := make(map[string]struct{}, len(p.dirtyItems)+len(state.items))
		for key := range p.dirtyItems {
			dirtyItems[key] = struct{}{}
		}
		for key := range state.items {
			dirtyItems[key] = struct{}{}
		}
		for key := range dirtyItems {
			item, ok := state.getItem(p, key)
			if !ok {
				if err := items.Delete([]byte(key)); err != nil {
					return err
				}
				continue
			}
			encoded, err := encodeDiskItem(item)
			if err != nil {
				return err
			}
			if err = items.Put([]byte(key), encoded); err != nil {
				return err
			}
		}
		dirtyGroups := make(map[string]struct{}, len(p.dirtyGroups)+len(state.groups))
		for group := range p.dirtyGroups {
			dirtyGroups[group] = struct{}{}
		}
		for group := range state.groups {
			dirtyGroups[group] = struct{}{}
		}
		for group := range dirtyGroups {
			keys, staged := state.groups[group]
			if !staged {
				keys = p.groups[group]
			}
			if len(keys) == 0 {
				if err := groups.Delete([]byte(group)); err != nil {
					return err
				}
				continue
			}
			values := make([]string, 0, len(keys))
			for key := range keys {
				values = append(values, key)
			}
			sort.Strings(values)
			encoded, err := encodeStrings(values)
			if err != nil {
				return err
			}
			if err = groups.Put([]byte(group), encoded); err != nil {
				return err
			}
		}
		return putUsedBytes(meta, state.used)
	})
}

func (p *provider) applyBatchLocked(state *batchState) {
	for key, staged := range state.items {
		if old, ok := p.items[key]; ok {
			p.removeReverseMappingLocked(key, old)
			p.removeItemAccountingLocked(old)
		}
		if !staged.present {
			delete(p.items, key)
			continue
		}
		item := staged.item
		if item.file {
			item.lru = p.lru.PushBack(key)
		}
		if !item.expiresAt.IsZero() {
			item.expiration = &expirationEntry{at: item.expiresAt, key: key, generation: item.generation, index: -1}
			heap.Push(&p.expirations, item.expiration)
		}
		p.items[key] = item
		p.addReverseMappingLocked(key, item)
		p.addItemAccountingLocked(item)
	}
	for group, keys := range state.groups {
		for key := range p.groups[group] {
			delete(p.itemGroups[key], group)
			if len(p.itemGroups[key]) == 0 {
				delete(p.itemGroups, key)
			}
		}
		if len(keys) == 0 {
			delete(p.groups, group)
			continue
		}
		p.groups[group] = keys
		for key := range keys {
			if p.itemGroups[key] == nil {
				p.itemGroups[key] = map[string]struct{}{}
			}
			p.itemGroups[key][group] = struct{}{}
		}
	}
	p.cacheUsed.Store(state.used)
	p.physicalUsed.Store(state.physical)
	p.expirationEntries.Store(uint64(len(p.expirations)))
	clear(p.dirtyItems)
	clear(p.dirtyGroups)
}

func (p *provider) addReverseMappingLocked(mappingKey string, item cacheItem) {
	if item.file || len(item.value) == 0 {
		return
	}
	for variant := range mappingVariants(item) {
		if p.variantMappings[variant] == nil {
			p.variantMappings[variant] = map[string]struct{}{}
		}
		p.variantMappings[variant][mappingKey] = struct{}{}
	}
}

func (p *provider) removeReverseMappingLocked(mappingKey string, item cacheItem) {
	if item.file || len(item.value) == 0 {
		return
	}
	for variant := range mappingVariants(item) {
		delete(p.variantMappings[variant], mappingKey)
		if len(p.variantMappings[variant]) == 0 {
			delete(p.variantMappings, variant)
		}
	}
}

func contentBodyFileName(key string, checksum [32]byte) string {
	return fmt.Sprintf("%s%x-%x", bodyPrefix, checksumKey(key), checksum)
}

func accountedItemSize(key string, item cacheItem) uint64 {
	size := itemIndexOverhead + uint64(len(key)) + uint64(len(item.value))
	if item.file {
		size += item.physicalSize
	}
	return size
}

func physicalFileSize(path string, logical uint64) uint64 {
	info, err := os.Stat(path)
	if err != nil {
		return roundUp4K(logical)
	}
	return physicalSizeFromInfo(info, logical)
}

func physicalSizeFromInfo(info os.FileInfo, logical uint64) uint64 {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Blocks > 0 {
		return uint64(stat.Blocks) * 512
	}
	return roundUp4K(logical)
}

func roundUp4K(value uint64) uint64 {
	if value == 0 {
		return 0
	}
	return (value + 4095) &^ uint64(4095)
}

func checksumKey(key string) [32]byte { return sha256.Sum256([]byte(key)) }

func encodeDiskItem(item cacheItem) ([]byte, error) {
	diskValue := diskItem{ExpiresAt: item.expiresAt, LastAccess: item.lastAccess, ModifiedAt: item.modifiedAt}
	if item.file {
		diskValue.File = filepath.Base(string(item.value))
		diskValue.CompressedSize = item.compressedSize
		diskValue.OriginalSize = item.originalSize
		diskValue.Checksum = item.checksum[:]
	} else {
		diskValue.Value = item.value
	}
	return json.Marshal(diskValue)
}

func encodeStrings(values []string) ([]byte, error) { return json.Marshal(values) }

func putUsedBytes(bucket *bolt.Bucket, used uint64) error {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], used)
	return bucket.Put(indexUsedBytesKey, encoded[:])
}
