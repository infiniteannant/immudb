/*
Copyright 2019-2020 vChain, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/
package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"sync"
	"time"

	"codenotary.io/immudb-v2/store"
)

func main() {
	dataDir := flag.String("dataDir", "data", "data directory")
	committers := flag.Int("committers", 10, "number of concurrent committers")
	parallelIO := flag.Int("parallelIO", 1, "number of parallel IO")
	txCount := flag.Int("txCount", 1_000, "number of tx to commit")
	kvCount := flag.Int("kvCount", 1_000, "number of kv entries per tx")
	kLen := flag.Int("kLen", 32, "key length (bytes)")
	vLen := flag.Int("vLen", 32, "value length (bytes)")
	txDelay := flag.Int("txDelay", 10, "delay (millis) between txs")
	printAfter := flag.Int("printAfter", 100, "print a dot '.' after specified number of committed txs")
	synced := flag.Bool("synced", false, "strict sync mode - no data lost")
	txRead := flag.Bool("txRead", false, "validate committed txs against input kv data")
	txLinking := flag.Bool("txLinking", true, "full scan to verify linear cryptographic linking between txs")
	kvInclusion := flag.Bool("kvInclusion", false, "validate kv data of every tx as part of the linear verification. txLinking must be enabled")
	logFileSize := flag.Int("logFileSize", 1<<26, "log file size up to which a new log file is created")
	openedLogFiles := flag.Int("openedLogFiles", 10, "number of maximun number of opened files per each log type")

	flag.Parse()

	fmt.Println("Opening Immutable Transactional Key-Value Log...")

	opts := store.DefaultOptions().
		SetSynced(*synced).
		SetIOConcurrency(*parallelIO).
		SetFileSize(*logFileSize).
		SetVLogMaxOpenedFiles(*openedLogFiles).
		SetTxLogMaxOpenedFiles(*openedLogFiles).
		SetCommitLogMaxOpenedFiles(*openedLogFiles)

	immuStore, err := store.Open(*dataDir, opts)

	if err != nil {
		panic(err)
	}

	defer func() {
		err := immuStore.Close()
		if err != nil {
			fmt.Printf("\r\nImmutable Transactional Key-Value Log closed with error: %v\r\n", err)
			return
		}
		fmt.Printf("\r\nImmutable Transactional Key-Value Log successfully closed!\r\n")
	}()

	fmt.Printf("Immutable Transactional Key-Value Log with %d Txs successfully opened!\r\n", immuStore.TxCount())

	fmt.Printf("Committing %d transactions...\r\n", *txCount)

	wgInit := &sync.WaitGroup{}
	wgInit.Add(*committers)

	wgWork := &sync.WaitGroup{}
	wgWork.Add(*committers)

	wgEnded := &sync.WaitGroup{}
	wgEnded.Add(*committers)

	wgStart := &sync.WaitGroup{}
	wgStart.Add(1)

	for c := 0; c < *committers; c++ {
		go func(id int) {
			txs := make([][]*store.KV, *txCount)

			for t := 0; t < *txCount; t++ {
				txs[t] = make([]*store.KV, *kvCount)

				rand.Seed(time.Now().UnixNano())

				for i := 0; i < *kvCount; i++ {
					k := make([]byte, *kLen)
					v := make([]byte, *vLen)

					rand.Read(k)
					rand.Read(v)

					txs[t][i] = &store.KV{Key: k, Value: v}
				}
			}

			fmt.Printf("\r\nCommitter %d is running...\r\n", id)

			wgInit.Done()

			wgStart.Wait()

			ids := make([]uint64, *txCount)

			for t := 0; t < *txCount; t++ {
				txid, _, _, _, err := immuStore.Commit(txs[t])
				if err != nil {
					panic(err)
				}

				ids[t] = txid

				if *printAfter > 0 && t%*printAfter == 0 {
					fmt.Print(".")
				}

				time.Sleep(time.Duration(*txDelay) * time.Millisecond)
			}

			wgWork.Done()
			fmt.Printf("\r\nCommitter %d done with commits!\r\n", id)

			if *txRead {
				fmt.Printf("Starting committed tx against input kv data by committer %d...\r\n", id)

				tx := store.NewTx(*kvCount, *kLen)
				b := make([]byte, *vLen)

				for i := range ids {
					immuStore.ReadTx(ids[i], tx)

					for ei, e := range tx.Entries() {
						if !bytes.Equal(e.Key(), txs[i][ei].Key) {
							panic(fmt.Errorf("committed tx key does not match input values"))
						}

						_, err = immuStore.ReadValueAt(b, e.VOff)
						if err != nil {
							panic(err)
						}

						if !bytes.Equal(b, txs[i][ei].Value) {
							panic(fmt.Errorf("committed tx value does not match input values"))
						}
					}
				}

				fmt.Printf("All committed txs successfully verified against input kv data by committer %d!\r\n", id)
			}

			wgEnded.Done()

			fmt.Printf("Committer %d sucessfully ended!\r\n", id)
		}(c)
	}

	wgInit.Wait()

	wgStart.Done()

	start := time.Now()
	wgWork.Wait()
	elapsed := time.Since(start)

	fmt.Printf("\r\nAll committers %d have successfully completed their work within %s!\r\n", *committers, elapsed)

	wgEnded.Wait()

	if *txLinking {
		fmt.Println("Starting full scan to verify linear cryptographic linking...")
		start := time.Now()

		txReader, err := immuStore.NewTxReader(0, 4096)
		if err != nil {
			panic(err)
		}

		verifiedTxs := 0

		b := make([]byte, immuStore.MaxValueLen())

		for {
			tx, err := txReader.Read()
			if err != nil {
				if err == io.EOF {
					break
				}
				panic(err)
			}

			txEntries := tx.Entries()

			if *kvInclusion {
				for i := 0; i < len(txEntries); i++ {
					path := tx.Proof(i)

					_, err = immuStore.ReadValueAt(b[:txEntries[i].ValueLen], txEntries[i].VOff)
					if err != nil {
						panic(err)
					}

					kv := &store.KV{Key: txEntries[i].Key(), Value: b[:txEntries[i].ValueLen]}

					verifies := path.VerifyInclusion(uint64(len(txEntries)-1), uint64(i), tx.Eh, kv.Digest())
					if !verifies {
						panic("kv does not verify")
					}
				}
			}

			verifiedTxs++

			if *printAfter > 0 && verifiedTxs%*printAfter == 0 {
				fmt.Print(".")
			}
		}

		elapsed := time.Since(start)
		fmt.Printf("\r\nAll transactions %d successfully verified in %s!\r\n", verifiedTxs, elapsed)
	}
}
