package sstable

// ============================================================
// SSTable Write Flow:
//
// Memtable (sorted)
//      ↓
// Split into blocks (~4KB each)
//      ↓
// Write blocks sequentially to file
//      ↓
// Build IndexEntry for each block:
//      - FirstKey
//      - Offset
//      - Length
//      ↓
// Write index section
//      ↓
// Write footer (indexOffset + numEntries)
//
// Final File:
//
//   [ Block 1 ][ Block 2 ] ... [ Index ][ Footer ]
//
// ============================================================
