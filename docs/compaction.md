# Compaction

Package: `compaction/` — files: `compact.go`, `group_by_similar_size.go`,
`is_similar_size.go`, `merge.go` (`compaction.go` is an empty package declaration)

## Why it exists

Every memtable flush creates a new SSTable. Without compaction the count grows forever, and
three things get worse in lockstep:

- **Read amplification** — a lookup for a missing key must check every table.
- **Space amplification** — an overwritten key keeps its old value in every older table;
  tombstones keep dead keys alive on disk.
- **Open cost** — every table's index and bloom filter must be held in memory.

Compaction merges tables together: fewer files, duplicates collapsed to the newest version.

## Strategy: size-tiered

We use **size-tiered compaction** (the Cassandra-style approach), not levelled (RocksDB-style).
Tables of *similar size* get merged into one bigger table. Small tables merge with small tables;
big ones eventually merge with other big ones once enough of them accumulate.

The trade-off versus levelled compaction: size-tiered has much lower **write amplification**
(each record is rewritten far fewer times) at the cost of higher **read** and **space**
amplification, since overlapping key ranges can exist across many tables simultaneously.

## Trigger

Called at the end of `db.flushMemtableLocked`, while `db.mu` is held:

```go
compacted, err := compaction.Compact(db.sstables, db.conf.SSTDir, db.watermark.Floor())
db.sstables = compacted
```

So compaction is **synchronous and inline with a flush** — writers are blocked for its whole
duration. There is no background compaction thread.

`Compact` returns immediately unless there are at least `DEFAULT_COMPACTION_THRESHOLD` (= 4)
tables:

```go
if len(tables) < config.DEFAULT_COMPACTION_THRESHOLD {
    return tables, nil
}
```

`floor` is `db.watermark.Floor()` — the oldest sequence number any snapshot obtained via
`db.GetSnapshot` still has pinned (`base.SeqNumMax` when nothing is pinned). It flows straight
through into `mergeIterators` (Step 2) and is the only thing standing between size-tiered
compaction and MVCC correctness: see below.

## Step 1 — group by similar size (`group_by_similar_size.go`)

`os.Stat` each table's file to get its byte size (a stat failure records size 0, which
`isSimilarSize` treats as never-similar, so the table is left alone).

Then a greedy O(n²) grouping pass:

```go
for i := range tables {
    if used[i] { continue }
    group := []*SSTable{tables[i]}; used[i] = true
    for j := i + 1; j < len(tables); j++ {
        if !used[j] && isSimilarSize(sizes[i], sizes[j]) {
            group = append(group, tables[j]); used[j] = true
        }
    }
    groups = append(groups, group)
}
```

Each unused table seeds a group and pulls in every later table within the size ratio. Note that
similarity is always measured against the **seed** table, not pairwise across the group — so a
group's members are all within 2× of the seed, but two members can be up to 4× apart.

### `isSimilarSize` (`is_similar_size.go`)

```go
if a == 0 || b == 0 { return false }
if a < b { a, b = b, a }
return a/b <= config.DEFAULT_SIMILAR_SIZE_RATIO    // 2
```

Larger divided by smaller, `<= 2`. It's **integer** division, so a 2.9× ratio still passes
(`2.9 → 2`); the effective threshold is "under 3×".

## Step 2 — merge each group (`merge.go`)

Groups of fewer than 2 tables are passed through untouched. Groups of 2+ go to `mergeGroup`:

```go
iterators := ...   // sstable.NewBlockIterator for each table — loads it fully into memory
merged := mergeIterators(iterators)
filePath := fmt.Sprintf("%s/%d.sst", dirPath, time.Now().UnixNano())
return sstable.WriteSSTableFromBlockEntries(filePath, merged)
```

### `mergeIterators(iterators, floor)` — newest wins, but keeps what snapshots still need

```go
keptAtOrBelowFloor := make(map[string]bool)
for _, it := range iterators {                 // iterators are in newest-first order
    for _, entry := range it.Entries {
        ik := base.DecodeInternalKey(entry.Key)
        key := string(ik.UserKey)

        if keptAtOrBelowFloor[key] { continue } // an older duplicate no snapshot can still reach
        result = append(result, entry)
        if ik.SeqNum() <= floor {
            keptAtOrBelowFloor[key] = true
        }
    }
}
sstable.SortEntries(result)
```

This is the MVCC-aware replacement for the old unconditional "keep only the newest version"
rule. `floor` is the oldest sequence number any active `db.GetSnapshot` snapshot still has
pinned (`base.Watermark.Floor`, `base.SeqNumMax` when nothing is pinned). For each user key,
every version **above** `floor` is kept unconditionally — some snapshot between `floor` and now
might need any one of them, and `Floor()` only reports the minimum pinned value, not the full set
of what's pinned. Once one version **at or below** `floor` has been kept, every older duplicate of
that same key is provably unreachable by any snapshot (the oldest surviving snapshot can't see
past `floor`, and this version already satisfies it) and gets dropped.

With no active snapshot, `floor` is `base.SeqNumMax`, so the very first (newest) occurrence of
every key always qualifies as "at or below floor" and the rule collapses back to the original
keep-only-the-newest behavior.

Conflict resolution is still purely positional beneath that: **`tables[0]` is the newest, so its
entries are considered first.** That ordering comes all the way from `sstable.OpenAllInDir`,
which sorts filenames — `UnixNano` timestamps — descending, and from `db.flushMemtableLocked`
prepending each new table to the front of the slice.

Despite the comment calling it a k-way merge, it isn't one: it concatenates all entries and
then does a single `sort.Slice` at the end. Correct, but O(N log N) instead of the O(N log k)
a real heap-based merge would give, and it holds every entry from every table in memory at once.

### Tombstones are always kept, floor or no floor

Regardless of `floor`, a tombstone is never dropped just for being "at or below floor" — the
code comments this explicitly, and it's the subtlest correctness point in the package:

> Tombstones are always kept regardless of floor — dropping one requires knowing no older
> SSTable still holds the key, which needs a manifest we don't have. Drop one early and a
> deleted key can resurrect.

Concretely: table A (new) has `tombstone(k)`, table C (old, not in this group) still has
`k → "v"`. Drop the tombstone while merging A, and a later `Get(k)` falls through to C and
returns `"v"` — a deleted key comes back to life. Tombstones can only be discarded during a
compaction that includes the *oldest* table containing the key, and there's no bookkeeping here
to establish that. So they accumulate — every tombstone still ends up in `result` via the same
loop, it just never gets the chance to be treated as droppable regardless of `floor`. Correct,
but space is never reclaimed from deletes.

## Step 3 — delete the merged-away files (`compact.go`)

```go
for _, sst := range tables {
    if compacted[sst.FilePath] {
        os.Remove(sst.FilePath)
    }
}
```

Deletion happens **after** every merged output has been written, fsync'd, and atomically
installed (`WriteSSTableFromBlockEntries` now writes to a `.tmp` file and renames it into place —
see [sstable.md](sstable.md#atomic-install-atomic_installgo)). Order matters: new file durable
first, old files removed second. A crash between the two leaves duplicate data — extra disk
usage, but reads stay correct because the newer file sorts first by name. A crash in the other
order would lose data. The atomic install means a crash *during* the write of the new file can no
longer leave a corrupt, half-written file at the final path — only that specific failure mode is
fixed by it, not the ordering argument above.

## Full flow

```
flushMemtableLocked
   └─ Compact(tables, dir, watermark.Floor())
        ├─ len < 4?  → return unchanged
        ├─ groupBySimilarSize     → [[t0,t3], [t1], [t2,t4,t5]]
        ├─ per group of 2+:
        │     NewBlockIterator ×n → mergeIterators(floor) → SortEntries
        │     → WriteSSTableFromBlockEntries → new <UnixNano>.sst  (fsync'd)
        ├─ singleton groups pass through
        └─ os.Remove every input file that was merged
```

## Known limitations

- **Merged groups can break the recency ordering.** This is a real correctness bug, not just a
  performance note. Groups are formed greedily from the newest table, so group 0 may contain
  both the newest table and a much older one; its merged output is appended to `result` first
  and therefore searched *before* tables from group 1 that are newer than that old member. A
  stale value can shadow a newer one. The result slice ordering is positional, not
  timestamp-derived, and the new file's `UnixNano` name doesn't help either — the in-memory
  `db.sstables` order is what reads use until the next restart. (After a restart,
  `OpenAllInDir` re-sorts by name, which puts the freshly-written merged file first — a
  different, also-not-necessarily-correct ordering.)
- **Fully synchronous.** Compaction runs while `db.mu` is held, so all reads and writes stall
  for its entire duration. Real engines do this in the background.
- **Whole tables are loaded into RAM.** `BlockIterator` materialises every entry of every table
  in the group; memory scales with total group size, not with a merge window. Keeping every
  version above `floor` (not just the newest) means a workload with many live snapshots can make
  a compacted table meaningfully larger than the old "one version per key" output — more data
  held in RAM during the merge and more disk written per compaction.
- **Not a streaming k-way merge** — concatenate-then-sort, O(N log N).
- **Tombstones are never reclaimed**, so deleted data occupies disk forever, independent of
  `floor` — see *Tombstones are always kept* above.
- **`Watermark.Floor` is a linear scan over active snapshots.** Fine at the scale hcdb expects
  (few concurrent snapshots), but doesn't scale the way a min-heap-based tracker (e.g. BadgerDB's)
  would — see [internal `base.Watermark`](db.md#snapshots-and-getat--scanat).
- **No manifest.** Recency is inferred from filenames and slice position. A manifest recording
  levels/generations would fix the ordering bug and enable safe tombstone dropping.
- **Partial failure across groups isn't atomic.** An error mid-way returns `nil`, having possibly
  already written and installed some merged output files from earlier groups in the loop, with no
  rollback. Note this is narrower than it used to be: a failure *within* one file's write no
  longer orphans a corrupt `.tmp` file (both writer functions now clean that up on error — see
  [sstable.md](sstable.md#atomic-install-atomic_installgo)) — the remaining gap is specifically
  about already-completed files from prior groups when a later group fails.

## Related

- [sstable.md](sstable.md) — `BlockIterator`, `WriteSSTableFromBlockEntries`, file naming
- [db.md](db.md) — where compaction is triggered and how `db.sstables` order drives reads
- [config.md](config.md) — `DEFAULT_COMPACTION_THRESHOLD`, `DEFAULT_SIMILAR_SIZE_RATIO`
