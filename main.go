package main

import (
	"fmt"

	"github.com/hchauhan7816/hcdb/wal"
)

func write(filePath string) {

	walObj, err := wal.Open(filePath)
	if err != nil {
		fmt.Println("Error in opening WAL file")
		return
	}
	defer walObj.File.Close()

	walObj.Put("harsh", "1")
	walObj.Put("chauhan", "2")

	if err := walObj.Sync(); err != nil {
		fmt.Println("Error in file sync")
		return
	}

	fmt.Println("Write Operation Done!")

}

func replay(filePath string) {

	fmt.Println("Starting Replay!")

	walObj, err := wal.Open(filePath)
	if err != nil {
		fmt.Println("Error opening file for replay")
	}

	entries, err := walObj.Replay()
	if err != nil {
		fmt.Println("Error in Replay")
	}

	for _, entry := range entries {
		fmt.Printf("Key => %s \t\t\t\t Value => %s \n", entry.Key, entry.Value)
	}
}

func main() {

	write("assets/main.wal")
	replay("assets/main.wal")

}
