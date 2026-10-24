// Command deal is the Anubandh deal chaincode (name "deal", channel "anubandh-ch").
// Compatible with Drunix: Drunix is a Hyperledger Fabric v2.5.x-compatible fork, so this is a
// standard Fabric Go chaincode (fabric-contract-api-go v1).
package main

import (
	"log"

	"github.com/hyperledger/fabric-contract-api-go/contractapi"
)

func main() {
	cc, err := contractapi.NewChaincode(&DealContract{})
	if err != nil {
		log.Panicf("create chaincode: %v", err)
	}
	if err := cc.Start(); err != nil {
		log.Panicf("start chaincode: %v", err)
	}
}
