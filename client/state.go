package client

import (
	"fmt"
	"github.com/bazo-blockchain/bazo-client/network"
	"github.com/bazo-blockchain/bazo-miner/miner"
	"github.com/bazo-blockchain/bazo-miner/p2p"
	"github.com/bazo-blockchain/bazo-miner/protocol"
	"time"
)

var (
	//All blockheaders of the whole chain
	blockHeaders []*protocol.Block

	activeParameters miner.Parameters

	UnsignedAccTx    = make(map[[32]byte]*protocol.AccTx)
	UnsignedConfigTx = make(map[[32]byte]*protocol.ConfigTx)
	UnsignedFundsTx  = make(map[[32]byte]*protocol.FundsTx)
)

//Update allBlockHeaders to the latest header
func sync() {
	update()
	network.Uptodate = true

	go incomingBlockHeaders()
}

func update() {
	time.Sleep(10 * time.Second)

	var loaded []*protocol.Block
	var youngest *protocol.Block

	youngest = loadBlockHeader(nil)
	if youngest == nil {
		logger.Fatal()
	}
	if len(blockHeaders) > 0 {
		loaded = checkForNewBlockHeaders(youngest, blockHeaders[len(blockHeaders)-1].Hash, loaded)
	} else {
		loaded = checkForNewBlockHeaders(youngest, [32]byte{}, loaded)
	}

	blockHeaders = append(blockHeaders, loaded...)
}

//Get new blockheaders recursively
func checkForNewBlockHeaders(latest *protocol.Block, lastLoaded [32]byte, loaded []*protocol.Block) []*protocol.Block {
	if latest.Hash != lastLoaded {

		logger.Printf("Header: %x loaded\n"+
			"NrFundsTx: %v\n"+
			"NrAccTx: %v\n"+
			"NrConfigTx: %v\n"+
			"NrStakeTx: %v\n",
			latest.Hash[:8],
			latest.NrFundsTx,
			latest.NrAccTx,
			latest.NrConfigTx,
			latest.NrConfigTx)

		var ancestor *protocol.Block
		ancestor = loadBlockHeader(latest.PrevHash[:])
		if ancestor == nil {
			//Try again
			ancestor = latest
		}

		loaded = checkForNewBlockHeaders(ancestor, lastLoaded, loaded)
		loaded = append(loaded, latest)
	}

	return loaded
}

func loadBlockHeader(blockHash []byte) (blockHeader *protocol.Block) {
	var errormsg string
	if blockHash != nil {
		errormsg = fmt.Sprintf("Loading block header %x failed: ", blockHash[:8])
	}

	err := network.BlockHeaderReq(blockHash[:])
	if err != nil {
		logger.Println(errormsg + err.Error())
		return nil
	}

	blockHeaderI, err := network.Fetch(network.BlockHeaderChan)
	if err != nil {
		logger.Println(errormsg + err.Error())
		return nil
	}

	blockHeader = blockHeaderI.(*protocol.Block)

	return blockHeader
}

func incomingBlockHeaders() {
	for {
		blockHeader := <-network.BlockHeaderIn
		blockHeaders = append(blockHeaders, blockHeader)
	}
}

func getState(acc *Account, lastTenTx []*FundsTxJson) (err error) {
	pubKeyHash := protocol.SerializeHashContent(acc.Address)

	//Get blocks if the Acc address:
	//* got issued as an Acc
	//* sent funds
	//* received funds
	//* is block's beneficiary
	//* nr of configTx in block is > 0 (in order to maintain params in light-client)
	relevantBlocks, err := getRelevantBlocks(acc.Address)

	for _, block := range relevantBlocks {
		if block != nil {
			//Collect block reward
			if block.Beneficiary == pubKeyHash {
				acc.Balance += activeParameters.Block_reward
			}

			//Balance funds and collect fee
			for _, txHash := range block.FundsTxData {
				err := network.TxReq(p2p.FUNDSTX_REQ, txHash)
				if err != nil {
					return err
				}

				txI, err := network.Fetch(network.FundsTxChan)
				if err != nil {
					return err
				}

				tx := txI.(protocol.Transaction)
				fundsTx := txI.(*protocol.FundsTx)

				if fundsTx.From == pubKeyHash || fundsTx.To == pubKeyHash || block.Beneficiary == pubKeyHash {
					//Validate tx
					if err := validateTx(block, tx, txHash); err != nil {
						return err
					}

					if fundsTx.From == pubKeyHash {
						//If Acc is no root, balance funds
						if !acc.IsRoot {
							acc.Balance -= fundsTx.Amount
							acc.Balance -= fundsTx.Fee
						}

						acc.TxCnt += 1
					}

					if fundsTx.To == pubKeyHash {
						acc.Balance += fundsTx.Amount

						put(lastTenTx, ConvertFundsTx(fundsTx, "verified"))
					}

					if block.Beneficiary == pubKeyHash {
						acc.Balance += fundsTx.Fee
					}
				}
			}

			//Check if Account was issued and collect fee
			for _, txHash := range block.AccTxData {
				err := network.TxReq(p2p.ACCTX_REQ, txHash)
				if err != nil {
					return err
				}

				txI, err := network.Fetch(network.AccTxChan)
				if err != nil {
					return err
				}

				tx := txI.(protocol.Transaction)
				accTx := txI.(*protocol.AccTx)

				if accTx.PubKey == acc.Address || block.Beneficiary == pubKeyHash {
					//Validate tx
					if err := validateTx(block, tx, txHash); err != nil {
						return err
					}

					if accTx.PubKey == acc.Address {
						acc.IsCreated = true
					}

					if block.Beneficiary == pubKeyHash {
						acc.Balance += accTx.Fee
					}
				}
			}

			//Update config parameters and collect fee
			for _, txHash := range block.ConfigTxData {
				err := network.TxReq(p2p.CONFIGTX_REQ, txHash)
				if err != nil {
					return err
				}

				txI, err := network.Fetch(network.ConfigTxChan)
				if err != nil {
					return err
				}

				tx := txI.(protocol.Transaction)
				configTx := txI.(*protocol.ConfigTx)

				configTxSlice := []*protocol.ConfigTx{configTx}

				if block.Beneficiary == pubKeyHash {
					//Validate tx
					if err := validateTx(block, tx, txHash); err != nil {
						return err
					}

					acc.Balance += configTx.Fee
				}

				miner.CheckAndChangeParameters(&activeParameters, &configTxSlice)
			}

			//TODO stakeTx

		}
	}

	addressHash := protocol.SerializeHashContent(acc.Address)
	for _, tx := range reqNonVerifiedTx(addressHash) {
		if tx.To == addressHash {
			put(lastTenTx, ConvertFundsTx(tx, "not verified"))
		}
		if tx.From == addressHash {
			acc.TxCnt++
		}
	}

	return nil
}
