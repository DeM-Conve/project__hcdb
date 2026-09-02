package main

import (
	"fmt"

	"github.com/hchauhan7816/hcdb/config"
	"github.com/hchauhan7816/hcdb/db"
)

// func write(filePath string) {

// 	walObj, err := wal.Open(filePath)
// 	if err != nil {
// 		fmt.Println("Error in opening WAL file")
// 		return
// 	}
// 	defer walObj.File.Close()

// 	walObj.Put("a", "1")
// 	walObj.Put("b", "2")
// 	walObj.Put("c", "3")
// 	walObj.Put("d", "4")
// 	walObj.Put("e", "5")
// 	walObj.Put("f", "6")
// 	walObj.Put("g", "7")
// 	walObj.Put("h", "8")
// 	walObj.Put("i", "9")
// 	walObj.Put("j", "10")
// 	walObj.Put("k", "11")
// 	walObj.Put("l", "12")
// 	walObj.Put("m", "13")
// 	walObj.Put("n", "14")
// 	walObj.Put("o", "15")
// 	walObj.Put("p", "16")
// 	walObj.Put("q", "17")
// 	walObj.Put("r", "18")
// 	walObj.Put("s", "19")
// 	walObj.Put("t", "20")
// 	walObj.Put("u", "21")
// 	walObj.Put("v", "22")
// 	walObj.Put("w", "23")
// 	walObj.Put("x", "24")
// 	walObj.Put("y", "25")
// 	walObj.Put("z", "26")
// 	walObj.Put("aa", "27")
// 	walObj.Put("ab", "28")
// 	walObj.Put("ac", "29")
// 	walObj.Put("ad", "30")
// 	walObj.Put("ae", "31")
// 	walObj.Put("af", "32")
// 	walObj.Put("ag", "33")
// 	walObj.Put("ah", "34")
// 	walObj.Put("ai", "35")
// 	walObj.Put("aj", "36")
// 	walObj.Put("ak", "37")
// 	walObj.Put("al", "38")
// 	walObj.Put("am", "39")
// 	walObj.Put("an", "40")
// 	walObj.Put("ao", "41")
// 	walObj.Put("ap", "42")
// 	walObj.Put("aq", "43")
// 	walObj.Put("ar", "44")
// 	walObj.Put("as", "45")
// 	walObj.Put("at", "46")
// 	walObj.Put("au", "47")
// 	walObj.Put("av", "48")
// 	walObj.Put("aw", "49")
// 	walObj.Put("ax", "50")
// 	walObj.Put("ay", "51")
// 	walObj.Put("az", "52")
// 	walObj.Put("ba", "53")
// 	walObj.Put("bb", "54")
// 	walObj.Put("bc", "55")
// 	walObj.Put("bd", "56")
// 	walObj.Put("be", "57")
// 	walObj.Put("bf", "58")
// 	walObj.Put("bg", "59")
// 	walObj.Put("bh", "60")
// 	walObj.Put("bi", "61")
// 	walObj.Put("bj", "62")
// 	walObj.Put("bk", "63")
// 	walObj.Put("bl", "64")
// 	walObj.Put("bm", "65")
// 	walObj.Put("bn", "66")
// 	walObj.Put("bo", "67")
// 	walObj.Put("bp", "68")
// 	walObj.Put("bq", "69")
// 	walObj.Put("br", "70")
// 	walObj.Put("bs", "71")
// 	walObj.Put("bt", "72")
// 	walObj.Put("bu", "73")
// 	walObj.Put("bv", "74")
// 	walObj.Put("bw", "75")
// 	walObj.Put("bx", "76")
// 	walObj.Put("by", "77")
// 	walObj.Put("bz", "78")
// 	walObj.Put("ca", "79")
// 	walObj.Put("cb", "80")
// 	walObj.Put("cc", "81")
// 	walObj.Put("cd", "82")
// 	walObj.Put("ce", "83")
// 	walObj.Put("cf", "84")
// 	walObj.Put("cg", "85")
// 	walObj.Put("ch", "86")
// 	walObj.Put("ci", "87")
// 	walObj.Put("cj", "88")
// 	walObj.Put("ck", "89")
// 	walObj.Put("cl", "90")
// 	walObj.Put("cm", "91")
// 	walObj.Put("cn", "92")
// 	walObj.Put("co", "93")
// 	walObj.Put("cp", "94")
// 	walObj.Put("cq", "95")
// 	walObj.Put("cr", "96")
// 	walObj.Put("cs", "97")
// 	walObj.Put("ct", "98")
// 	walObj.Put("cu", "99")
// 	walObj.Put("cv", "100")
// 	walObj.Put("Harsh", "Chauhan")
// 	walObj.Put("H", "arsh")
// 	walObj.Put("Ha", "rsh")
// 	walObj.Put("Harsh", "Chauhan")

// 	// for i := 0; i < 10_000_000; i++ {
// 	// 	walObj.Put("key", strings.Repeat("x", 1024))
// 	// }

// 	if err := walObj.Sync(); err != nil {
// 		fmt.Println("Error in file sync")
// 		return
// 	}

// 	fmt.Println("Write Operation Done!")

// }

// func replay(filePath string) {

// 	fmt.Println("Starting Replay!")

// 	walObj, err := wal.Open(filePath)
// 	if err != nil {
// 		fmt.Println("Error opening file for replay:", err)
// 		return
// 	}

// 	entries, err := walObj.Replay()
// 	if err != nil {
// 		fmt.Println(err)
// 		fmt.Println("Error in Replay!")
// 		return
// 	}

// 	for _, entry := range entries {
// 		fmt.Printf("Key => %s \t\t\t\t Value => %s \n", entry.Key, entry.Value)
// 	}
// }

func main() {
	database, err := db.Open(config.Config{WALPath: "assets/main.wal", SSTDir: "assets/sstables"})
	if err != nil {
		fmt.Println("Error:", err)
		return
	}
	defer database.Close()

	database.Put("harsh", "chauhan")
	database.Put("a", "1")
	database.Put("b", "2")
	database.ForceFlush()
	fmt.Println("flush 1 done")

	database.Put("harsh", "engineer")
	database.Put("c", "3")
	database.Put("d", "4")
	database.ForceFlush()
	fmt.Println("flush 2 done")

	database.Put("e", "5")
	database.Put("f", "6")
	database.Delete("b") // delete from earlier batch
	database.ForceFlush()
	fmt.Println("flush 3 done")

	database.Put("g", "7")
	database.Put("h", "8")
	database.ForceFlush()
	fmt.Println("flush 4 done — compaction triggered")

	fmt.Println("\n--- reads after compaction ---")
	printGet(database, "harsh") // Engineer — latest wins
	printGet(database, "a")     // 1
	printGet(database, "b")     // not found — deleted
	printGet(database, "c")     // 3
	printGet(database, "g")     // 7

	fmt.Println("\n--- range scan [a, h] across memtable + all sstables ---")
	printScan(database, []byte("a"), []byte("h"))
}

func printScan(database *db.DB, lowerBound, upperBound []byte) {
	it, err := database.Scan(lowerBound, upperBound)
	if err != nil {
		fmt.Println("scan error:", err)
		return
	}

	for it.Next() {
		fmt.Printf("%s => %s\n", it.Key(), it.Value())
	}
}

func printGet(database *db.DB, key string) {
	if val, ok := database.Get(key); ok {
		fmt.Printf("%s => %s\n", key, val)
	} else {
		fmt.Printf("%s => not found\n", key)
	}
}
