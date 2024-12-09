package main

import (
    "encoding/hex"
    "fmt"
    "github.com/ethereum/go-ethereum/core/tracing"
    "github.com/ethereum/go-ethereum/triedb"
    "github.com/holiman/uint256"
    "math/big"

    "github.com/ethereum/go-ethereum/common"
    "github.com/ethereum/go-ethereum/core/rawdb"
    "github.com/ethereum/go-ethereum/core/state"
    "github.com/ethereum/go-ethereum/core/state/snapshot"
    "github.com/ethereum/go-ethereum/crypto"

    solsha3 "github.com/miguelmota/go-solidity-sha3"
)

var toHash = common.BytesToHash

func main() {
    var snaps *snapshot.Tree
    stateDB, _ := state.New(common.Hash{}, state.NewDatabase(triedb.NewDatabase(rawdb.NewMemoryDatabase(), nil), snaps))
    // stateDB, _ := state.New(common.Hash{}, state.NewDatabase(rawdb.NewMemoryDatabase()), snaps)

    account1 := common.HexToAddress("0x1111111111111111111111111111111111111111")
    account2 := common.HexToAddress("0x2222222222222222222222222222222222222222")

    stateDB.AddBalance(account1, &uint256.Int{2000}, tracing.BalanceChangeUnspecified)
    stateDB.AddBalance(account2, &uint256.Int{8888}, tracing.BalanceIncreaseRewardMineUncle)
    //stateDB.AddBalance(account1, big.NewInt(2000))
    //stateDB.AddBalance(account2, big.NewInt(8888))
    contract := crypto.CreateAddress(account1, stateDB.GetNonce(account1))
    stateDB.CreateAccount(contract)
    stateDB.SetCode(contract, []byte("contract code bytes"))

    stateDB.SetNonce(contract, 1)
    stateDB.SetState(contract, toHash([]byte("owner")), toHash(account1.Bytes()))
    stateDB.SetState(contract, toHash([]byte("name")), toHash([]byte("hsy")))

    stateDB.SetState(contract, toHash([]byte("online")), toHash([]byte{1}))
    stateDB.SetState(contract, toHash([]byte("online")), toHash([]byte{}))

    stateDB.Commit(0, true)

    // TODO Geth changed this API.
    // fmt.Println(string(stateDB.Dump(true, true, true)))

    fmt.Println("------Test Hash-------")
    for i := 0; i <= 2; i++ {
        hash := solsha3.SoliditySHA3(
            solsha3.Uint256(big.NewInt(int64(i))),
        )
        fmt.Printf("The hash of slot pos %d:   0x%x\n", i, hash)
    }

    // Test the map-type in contract storage
    // Map key/ Slot postion
    k1 := solsha3.SoliditySHA3([]byte("hsy"), solsha3.Uint256(big.NewInt(int64(1))))
    fmt.Printf("Test the Solidity Map storage Key1:         0x%x\n", k1)

    // Corresponding Object slot index
    i1 := solsha3.SoliditySHA3(solsha3.Uint256(hexToBigInt(hex.EncodeToString(k1))))
    fmt.Printf("Test the Solidity Map storage Key1's index: 0x%x\n", i1)

    k2 := solsha3.SoliditySHA3([]byte("lei"), solsha3.Uint256(big.NewInt(int64(1))))
    fmt.Printf("Test the Solidity Map storage Key2:         0x%x\n", k2)

    i2 := solsha3.SoliditySHA3(solsha3.Uint256(hexToBigInt(hex.EncodeToString(k2))))
    fmt.Printf("Test the Solidity Map storage Key2's index: 0x%x\n", i2)

}

func hexToBigInt(hex string) *big.Int {
    n := new(big.Int)
    n, _ = n.SetString(hex[:], 16)

    return n
}
