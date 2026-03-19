package sstable

// ============================================================
// SSTable Read Flow:
//
// 1. Open file
// 2. Read footer (last 12 bytes)
//      → get indexOffset and numEntries
// 3. Load index into memory
// 4. Binary search index to find correct block
// 5. Read only that block from disk
// 6. Scan block entries to find key
//
// ------------------------------------------------------------
// Lookup Flow:
//
//   key → searchIndex() → blockIdx
//       → readBlock(offset, length)
//       → decodeBlock()
//       → findInBlock()
//
// ------------------------------------------------------------
// Key Insight:
//
// - IndexEntry → helps locate block (log N)
// - BlockEntry → actual data (scan within block)
//
// ============================================================

// Binary search on index entries.
// Returns the index of the block whose FirstKey <= target key.
//
// Example:
//
// Index:
//   [a, d, g]
//
// Search key = "e"
//
// Result:
//   returns index of "d" → block 2
//
