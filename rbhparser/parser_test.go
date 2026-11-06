package rbhparser

import (
	"errors"
	"math/big"
	"reflect"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	gethtypes "github.com/ethereum/go-ethereum/core/types"
)

func abiArguments(t testing.TB, names ...string) abi.Arguments {
	t.Helper()
	args := make(abi.Arguments, len(names))
	for i, name := range names {
		typ, err := abi.NewType(name, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		args[i] = abi.Argument{Type: typ}
	}
	return args
}

func addressTopic(address common.Address) common.Hash { return common.BytesToHash(address.Bytes()) }

func makeInitializeLog(t testing.TB, key PoolKey, index uint) gethtypes.Log {
	t.Helper()
	id, err := PoolID(key)
	if err != nil {
		t.Fatal(err)
	}
	data, err := abiArguments(t, "uint24", "int24", "address", "uint160", "int24").Pack(new(big.Int).SetUint64(uint64(key.Fee)), big.NewInt(int64(key.TickSpacing)), key.Hooks, big.NewInt(1).Lsh(big.NewInt(1), 96), big.NewInt(0))
	if err != nil {
		t.Fatal(err)
	}
	return gethtypes.Log{Address: DefaultAddressBook().PoolManager, Topics: []common.Hash{TopicPoolInitialize, id, addressTopic(key.Currency0), addressTopic(key.Currency1)}, Data: data, Index: index}
}

func makeSwapLog(t testing.TB, key PoolKey, index uint) gethtypes.Log {
	t.Helper()
	id, err := PoolID(key)
	if err != nil {
		t.Fatal(err)
	}
	data, err := abiArguments(t, "int128", "int128", "uint160", "uint128", "int24", "uint24").Pack(big.NewInt(100), big.NewInt(-50), big.NewInt(1).Lsh(big.NewInt(1), 96), big.NewInt(1000), big.NewInt(-4), big.NewInt(3000))
	if err != nil {
		t.Fatal(err)
	}
	return gethtypes.Log{Address: DefaultAddressBook().PoolManager, Topics: []common.Hash{TopicPoolSwap, id, addressTopic(common.HexToAddress("0x44"))}, Data: data, Index: index}
}

func makeBagsLaunchLog(t testing.TB, token, curve common.Address, poolID common.Hash, index uint) gethtypes.Log {
	t.Helper()
	data, err := bagsEventABI.Events["TokenCreated"].Inputs.NonIndexed().Pack(
		common.HexToAddress("0x61"),
		common.HexToAddress("0x62"),
		poolID,
		"Bags Test",
		"BAGS",
		"ipfs://metadata",
	)
	if err != nil {
		t.Fatal(err)
	}
	return gethtypes.Log{
		Address: DefaultAddressBook().BagsFactory,
		Topics: []common.Hash{
			TopicBagsCreated,
			addressTopic(token),
			addressTopic(curve),
			addressTopic(common.HexToAddress("0x63")),
		},
		Data:  data,
		Index: index,
	}
}

func TestParsePAIRMultipleQuotePoolsInOneReceipt(t *testing.T) {
	parsed := New()
	token := common.HexToAddress("0x20")
	quotes := []common.Address{common.HexToAddress("0x10"), common.HexToAddress("0x11")}
	logs := make([]*gethtypes.Log, 0, 4)
	ids := make([]common.Hash, 0, 2)
	for index, quote := range quotes {
		key := PoolKey{Currency0: quote, Currency1: token, Fee: 3000, TickSpacing: 60}
		id, err := PoolID(key)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		initialized := makeInitializeLog(t, key, uint(index*2+1))
		data, err := abiArguments(t, "uint256", "uint16", "uint256", "int24", "int24", "uint160", "uint256").Pack(
			big.NewInt(1), uint16(100), big.NewInt(1), big.NewInt(-60), big.NewInt(60), big.NewInt(1), big.NewInt(1),
		)
		if err != nil {
			t.Fatal(err)
		}
		launched := gethtypes.Log{
			Address: DefaultAddressBook().PAIRLaunchpad,
			Topics:  []common.Hash{TopicPAIRPool, addressTopic(token), addressTopic(quote), id},
			Data:    data,
			Index:   uint(index*2 + 2),
		}
		logs = append(logs, &initialized, &launched)
	}

	events, err := parsed.ParseReceipt(&gethtypes.Receipt{Logs: logs})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 4 {
		t.Fatalf("events = %d, want 4", len(events))
	}
	for index, id := range ids {
		registration, ok := parsed.Registry().LookupPool(id)
		if !ok || registration.Protocol != ProtocolPAIR || registration.Token != token || registration.Quote != quotes[index] {
			t.Fatalf("PAIR pool %d missing or misattributed: %#v ok=%t", index, registration, ok)
		}
	}
	if _, err := parsed.ParseReceipt(&gethtypes.Receipt{Logs: logs}); err != nil {
		t.Fatalf("idempotent receipt replay: %v", err)
	}
}

func TestParseLongLaunchInitializeAndFirstSwap(t *testing.T) {
	parser := New()
	addresses := DefaultAddressBook()
	quote := common.HexToAddress("0x10")
	token := common.HexToAddress("0x20")
	key := PoolKey{Currency0: quote, Currency1: token, Fee: 0x800000, TickSpacing: 8, Hooks: common.HexToAddress("0x4e3468951D49f2EEa976eD0D6e75fFCb44a9a544")}
	id, err := PoolID(key)
	if err != nil {
		t.Fatal(err)
	}
	longData, err := longEventABI.Events["LaunchCreated"].Inputs.NonIndexed().Pack(key.Hooks, common.HexToAddress("0x55"), [32]byte{1}, big.NewInt(100), big.NewInt(86500), "LONG")
	if err != nil {
		t.Fatal(err)
	}
	launchLog := gethtypes.Log{Address: addresses.LongLauncher, Topics: []common.Hash{TopicLongLaunch, addressTopic(token), addressTopic(token), addressTopic(quote)}, Data: longData, Index: 1}
	initializeLog := makeInitializeLog(t, key, 2)
	swapLog := makeSwapLog(t, key, 3)
	receipt := &gethtypes.Receipt{Logs: []*gethtypes.Log{&launchLog, &initializeLog, &swapLog}}
	events, err := parser.ParseReceipt(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("events = %d, want 3", len(events))
	}
	launch := events[0].Data.(Launch)
	if launch.Protocol != ProtocolLong || launch.Token != token || launch.Quote != quote || launch.Pool == nil || launch.PoolID != id || launch.Pool.Hooks != key.Hooks {
		t.Fatalf("unexpected launch: %#v", launch)
	}
	if events[1].Protocol != ProtocolLong || events[2].Protocol != ProtocolLong || events[2].Kind != EventSwap {
		t.Fatalf("protocol association failed: %#v", events)
	}
	registration, ok := parser.Registry().LookupPool(id)
	if !ok || registration.Protocol != ProtocolLong || registration.Token != token {
		t.Fatalf("pool was not registered: %#v", registration)
	}
}

func TestParseLetsCashLaunchFeeInitializeAndFirstSwap(t *testing.T) {
	parser := New()
	addresses := DefaultAddressBook()
	token := common.HexToAddress("0x7a5204507d139b7107a2370011749c11a9b37ecc")
	creator := common.HexToAddress("0x6478e032c1680e777d60fea9370699c463361a4c")
	key := PoolKey{Currency0: common.Address{}, Currency1: token, TickSpacing: 200, Hooks: addresses.LetsCashHook}
	id, err := PoolID(key)
	if err != nil {
		t.Fatal(err)
	}
	feeData, err := abiArguments(t, "address", "uint256", "uint256", "bool", "bool").Pack(creator, big.NewInt(9_400), big.NewInt(50_000), true, false)
	if err != nil {
		t.Fatal(err)
	}
	launchData, err := abiArguments(t, "uint256", "uint256", "uint256", "address", "address").Pack(big.NewInt(1_004), big.NewInt(1), big.NewInt(2), addresses.LetsCashHook, creator)
	if err != nil {
		t.Fatal(err)
	}
	feeLog := gethtypes.Log{Address: addresses.LetsCashHook, Topics: []common.Hash{TopicLetsCashFee, id, addressTopic(creator)}, Data: feeData, Index: 1}
	initialize := makeInitializeLog(t, key, 2)
	launch := gethtypes.Log{Address: addresses.LetsCashFactory, Topics: []common.Hash{TopicLetsCashLaunch, addressTopic(token), addressTopic(creator), id}, Data: launchData, Index: 4}
	swap := makeSwapLog(t, key, 3)
	events, err := parser.ParseReceipt(&gethtypes.Receipt{Logs: []*gethtypes.Log{&feeLog, &initialize, &swap, &launch}})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 4 || events[0].Kind != EventFeePolicy || events[0].Protocol != ProtocolLetsCash || events[2].Kind != EventSwap || events[2].Protocol != ProtocolLetsCash || events[3].Kind != EventLaunch {
		t.Fatalf("unexpected events: %#v", events)
	}
	policy := events[0].Data.(FeePolicy)
	if policy.PoolID != id || policy.BPS() != 500 || policy.BuyFeeBPS != 500 {
		t.Fatalf("unexpected fee policy: %#v", policy)
	}
	registration, ok := parser.Registry().LookupPool(id)
	if !ok || registration.Protocol != ProtocolLetsCash || registration.Token != token || registration.Quote != (common.Address{}) {
		t.Fatalf("unexpected registration: %#v ok=%v", registration, ok)
	}
}

func flapCreationLogs(t testing.TB, token common.Address, version uint8, buyTax, sellTax uint32) []*gethtypes.Log {
	t.Helper()
	portal := DefaultAddressBook().FlapController
	created, err := flapEventABI.Events["TokenCreated"].Inputs.Pack(big.NewInt(1_700_000_000), common.HexToAddress("0x1234"), big.NewInt(7), token, "Flap", "FLAP", "bafy")
	if err != nil {
		t.Fatal(err)
	}
	pack := func(types []string, values ...any) []byte {
		data, packErr := abiArguments(t, types...).Pack(values...)
		if packErr != nil {
			t.Fatal(packErr)
		}
		return data
	}
	logs := []*gethtypes.Log{
		{Address: portal, Topics: []common.Hash{TopicFlapLaunch}, Data: created, Index: 1},
		{Address: portal, Topics: []common.Hash{TopicFlapCurve}, Data: pack([]string{"address", "uint256", "uint256", "uint256"}, token, big.NewInt(1), big.NewInt(2), big.NewInt(3)), Index: 2},
		{Address: portal, Topics: []common.Hash{TopicFlapDexThreshold}, Data: pack([]string{"address", "uint256"}, token, big.NewInt(4)), Index: 3},
		{Address: portal, Topics: []common.Hash{TopicFlapVersion}, Data: pack([]string{"address", "uint8"}, token, version), Index: 4},
		{Address: portal, Topics: []common.Hash{TopicFlapQuote}, Data: pack([]string{"address", "address"}, token, common.Address{}), Index: 5},
		{Address: portal, Topics: []common.Hash{TopicFlapMigrator}, Data: pack([]string{"address", "uint8"}, token, uint8(1)), Index: 6},
		{Address: portal, Topics: []common.Hash{TopicFlapDexPreference}, Data: pack([]string{"address", "uint8", "uint8"}, token, uint8(0), uint8(0)), Index: 7},
	}
	if version == flapTaxV3Version {
		logs = append(logs,
			&gethtypes.Log{Address: portal, Topics: []common.Hash{TopicFlapTax}, Data: pack([]string{"address", "uint256"}, token, new(big.Int).SetUint64(uint64(max(buyTax, sellTax)))), Index: 8},
			&gethtypes.Log{Address: portal, Topics: []common.Hash{TopicFlapAsymmetricTax}, Data: pack([]string{"address", "uint256", "uint256"}, token, new(big.Int).SetUint64(uint64(buyTax)), new(big.Int).SetUint64(uint64(sellTax))), Index: 9},
		)
	}
	return logs
}

func TestParseFlapTaxV3CreationAndTaxFreeTransition(t *testing.T) {
	parser := New()
	token := common.HexToAddress("0x1347d0f767b69c7ce7cedece46f6b1f742567777")
	events, err := parser.ParseReceipt(&gethtypes.Receipt{Logs: flapCreationLogs(t, token, flapTaxV3Version, 200, 300)})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Protocol != ProtocolFlapTax || events[0].Kind != EventLaunch || events[1].Kind != EventFeePolicy {
		t.Fatalf("unexpected Flap events: %#v", events)
	}
	launch := events[0].Data.(Launch)
	policy := events[1].Data.(FeePolicy)
	if launch.TokenVersion != flapTaxV3Version || launch.MigratorType != flapV2Migrator || launch.DexID != flapUniswapDEX || launch.Venue != parser.Addresses().FlapController || !launch.FeesVerified || policy.BuyFeeBPS != 200 || policy.SellFeeBPS != 300 {
		t.Fatalf("unexpected Flap launch=%#v policy=%#v", launch, policy)
	}
	registration, ok := parser.Registry().LookupToken(token)
	if !ok || registration.Protocol != ProtocolFlapTax || registration.Venue != parser.Addresses().FlapController {
		t.Fatalf("unexpected Flap token registration: %#v ok=%v", registration, ok)
	}
	stateData, err := abiArguments(t, "uint8", "uint8").Pack(uint8(3), uint8(4))
	if err != nil {
		t.Fatal(err)
	}
	event, ok, err := parser.ParseLog(gethtypes.Log{Address: token, Topics: []common.Hash{TopicFlapPoolState}, Data: stateData})
	if err != nil || !ok || event.Kind != EventFeePolicy || event.Data.(FeePolicy).BuyFeeBPS != 0 {
		t.Fatalf("Flap tax-free event=%#v ok=%v err=%v", event, ok, err)
	}
}

func TestParseFlapNonTaxCreation(t *testing.T) {
	parser := New()
	token := common.HexToAddress("0xa568029a9da28ba16bd4b29dbd78490cc0d87777")
	events, err := parser.ParseReceipt(&gethtypes.Receipt{Logs: flapCreationLogs(t, token, flapNonTaxVersion, 0, 0)})
	if err != nil || len(events) != 2 || events[0].Protocol != ProtocolFlapStocks || events[1].Data.(FeePolicy).BuyFeeBPS != 0 {
		t.Fatalf("Flap non-tax events=%#v err=%v", events, err)
	}
}

func TestParseReceiptPreservesStandaloneFlapTaxUpdate(t *testing.T) {
	parser := New()
	token := common.HexToAddress("0x6100000000000000000000000000000000007777")
	if err := parser.Registry().RegisterToken(TokenRegistration{Token: token, Protocol: ProtocolFlapTax}); err != nil {
		t.Fatal(err)
	}
	data, err := abiArguments(t, "address", "uint256", "uint256").Pack(token, big.NewInt(275), big.NewInt(125))
	if err != nil {
		t.Fatal(err)
	}
	receipt := &gethtypes.Receipt{Status: gethtypes.ReceiptStatusSuccessful, Logs: []*gethtypes.Log{{
		Address: parser.Addresses().FlapController,
		Topics:  []common.Hash{TopicFlapAsymmetricTax},
		Data:    data,
		Index:   7,
	}}}
	events, err := parser.ParseReceipt(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Kind != EventFeePolicy || events[0].Protocol != ProtocolFlapTax {
		t.Fatalf("standalone Flap tax events = %#v", events)
	}
	policy := events[0].Data.(FeePolicy)
	if policy.Token != token || policy.BuyFeeBPS != 275 || policy.SellFeeBPS != 125 {
		t.Fatalf("standalone Flap tax policy = %#v", policy)
	}
}

func TestParseReceiptFlapConsumptionUsesReceiptPosition(t *testing.T) {
	parser := New()
	createdToken := common.HexToAddress("0x6200000000000000000000000000000000007777")
	updatedToken := common.HexToAddress("0x6300000000000000000000000000000000007777")
	if err := parser.Registry().RegisterToken(TokenRegistration{Token: updatedToken, Protocol: ProtocolFlapTax}); err != nil {
		t.Fatal(err)
	}
	logs := flapCreationLogs(t, createdToken, flapTaxV3Version, 200, 200)
	data, err := abiArguments(t, "address", "uint256", "uint256").Pack(updatedToken, big.NewInt(275), big.NewInt(125))
	if err != nil {
		t.Fatal(err)
	}
	logs = append(logs, &gethtypes.Log{
		Address: parser.Addresses().FlapController, Topics: []common.Hash{TopicFlapAsymmetricTax}, Data: data,
		Index: logs[0].Index,
	})
	events, err := parser.ParseReceipt(&gethtypes.Receipt{Status: gethtypes.ReceiptStatusSuccessful, Logs: logs})
	if err != nil {
		t.Fatal(err)
	}
	var updateFound bool
	for _, event := range events {
		policy, ok := event.Data.(FeePolicy)
		if ok && policy.Token == updatedToken && policy.BuyFeeBPS == 275 && policy.SellFeeBPS == 125 {
			updateFound = true
		}
	}
	if !updateFound {
		t.Fatalf("standalone tax update sharing a log index was consumed: %#v", events)
	}
}

func TestParseKnownProxyUpgrade(t *testing.T) {
	parser := New()
	implementation := common.HexToAddress("0x123456")
	for _, test := range []struct {
		name     string
		proxy    common.Address
		protocol Protocol
	}{
		{name: "GMGN", proxy: parser.Addresses().GMGNRouter, protocol: ProtocolUnknown},
		{name: "Varo", proxy: parser.Addresses().VaroLaunchpad, protocol: ProtocolVaro},
	} {
		t.Run(test.name, func(t *testing.T) {
			event, ok, err := parser.ParseLog(gethtypes.Log{
				Address: test.proxy,
				Topics:  []common.Hash{TopicProtocolUpgraded, addressTopic(implementation)},
			})
			if err != nil || !ok || event.Kind != EventProtocolUpgrade || event.Protocol != test.protocol {
				t.Fatalf("upgrade event=%#v ok=%v err=%v", event, ok, err)
			}
			upgrade := event.Data.(ProtocolUpgrade)
			if upgrade.Proxy != test.proxy || upgrade.Implementation != implementation {
				t.Fatalf("unexpected upgrade: %#v", upgrade)
			}
		})
	}
}

func TestParseVaroLaunchAndProtocolFee(t *testing.T) {
	parser := New()
	token := common.HexToAddress("0xc1a036b7252289b24fc3a6a778b18bd6f4182b06")
	creator := common.HexToAddress("0x9d06e3f0b940160ff928dcf7d3473a303c601f9c")
	venue := common.HexToAddress("0x225ef630877b53d70e1ddb2bd06c53918297fe14")
	launchProtocol := common.HexToAddress("0x13618c823991c82971ed0e2da160c391d1d13c0f")
	quote := parser.Addresses().WETH
	data, err := abiArguments(t, "address", "address", "address", "uint256", "uint256", "uint256", "uint256", "int256", "uint256", "uint256", "uint256", "uint16").Pack(
		common.HexToAddress("0xec851456e1c2ff81f9535d326b3b4720e382aed3"), venue, quote,
		big.NewInt(1), big.NewInt(2), big.NewInt(3), big.NewInt(4), big.NewInt(-88720), big.NewInt(139_800), big.NewInt(10_000), big.NewInt(6), uint16(2_000),
	)
	if err != nil {
		t.Fatal(err)
	}
	event, ok, err := parser.ParseLog(gethtypes.Log{Address: parser.Addresses().VaroLaunchpad, Topics: []common.Hash{TopicVaroLaunch, common.BigToHash(big.NewInt(1)), addressTopic(token), addressTopic(creator)}, Data: data})
	if err != nil || !ok {
		t.Fatalf("Varo event=%#v ok=%v err=%v", event, ok, err)
	}
	launch := event.Data.(Launch)
	if launch.Venue != venue || launch.Quote != quote || !launch.FeesVerified || launch.BuyFeeBPS != 2_000 || launch.SellFeeBPS != 2_000 {
		t.Fatalf("unexpected Varo launch: %#v", launch)
	}
	feeData, err := abiArguments(t, "uint16", "uint16").Pack(uint16(2_000), uint16(250))
	if err != nil {
		t.Fatal(err)
	}
	event, ok, err = parser.ParseLog(gethtypes.Log{Address: parser.Addresses().VaroLaunchpad, Topics: []common.Hash{TopicVaroFee, addressTopic(token), addressTopic(launchProtocol)}, Data: feeData})
	if err != nil || !ok || event.Kind != EventFeePolicy || event.Data.(FeePolicy).BuyFeeBPS != 250 {
		t.Fatalf("Varo fee event=%#v ok=%v err=%v", event, ok, err)
	}

	for _, test := range []struct {
		name   string
		topics []common.Hash
		data   []byte
	}{
		{name: "truncated", topics: []common.Hash{TopicVaroFee, addressTopic(token), addressTopic(launchProtocol)}, data: feeData[:32]},
		{name: "zero launch protocol", topics: []common.Hash{TopicVaroFee, addressTopic(token), common.Hash{}}, data: feeData},
		{name: "above contract maximum", topics: []common.Hash{TopicVaroFee, addressTopic(token), addressTopic(launchProtocol)}, data: func() []byte {
			data, packErr := abiArguments(t, "uint16", "uint16").Pack(uint16(250), uint16(3_001))
			if packErr != nil {
				t.Fatal(packErr)
			}
			return data
		}()},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, ok, err := parser.ParseLog(gethtypes.Log{Address: parser.Addresses().VaroLaunchpad, Topics: test.topics, Data: test.data}); err == nil || ok {
				t.Fatalf("malformed Varo fee event accepted: ok=%v err=%v", ok, err)
			}
		})
	}
}

func TestParseVirtualsPreLaunchIncludesAntiSniperPolicy(t *testing.T) {
	parser := New()
	token := common.HexToAddress("0x222aeb6973866995f733f61c9ab1ed058986efcb")
	venue := common.HexToAddress("0x8a95c7239680e9c4fd8366ea9320fe2c86056c99")
	data, err := abiArguments(t, "uint256", "uint256", "uint8", "uint16", "bool", "uint8", "bool").Pack(big.NewInt(1), new(big.Int), uint8(0), uint16(500), true, uint8(1), false)
	if err != nil {
		t.Fatal(err)
	}
	event, ok, err := parser.ParseLog(gethtypes.Log{Address: parser.Addresses().VirtualsLaunchpad, Topics: []common.Hash{TopicVirtualsLaunch, addressTopic(token), addressTopic(venue)}, Data: data})
	if err != nil || !ok {
		t.Fatalf("Virtuals event=%#v ok=%v err=%v", event, ok, err)
	}
	launch := event.Data.(Launch)
	if launch.Venue != venue || launch.Quote != parser.Addresses().VirtualToken || !launch.PreLaunch || launch.AntiSniperTaxType != 1 || launch.AirdropBPS != 500 || !launch.NeedACF || launch.FeesVerified {
		t.Fatalf("unexpected Virtuals launch: %#v", launch)
	}
}

func TestParseVirtualsLaunchedAndTaxUpdate(t *testing.T) {
	parser := New()
	token := common.HexToAddress("0xed6cac056c3127b89da25d6b06d7aaa0f0f37766")
	venue := common.HexToAddress("0x8a95c7239680e9c4fd8366ea9320fe2c86056c99")
	data, err := abiArguments(t, "uint256", "uint256", "uint256", "uint8", "uint16", "bool", "uint8", "bool").Pack(big.NewInt(1), big.NewInt(2), big.NewInt(3), uint8(0), uint16(0), false, uint8(3), true)
	if err != nil {
		t.Fatal(err)
	}
	event, ok, err := parser.ParseLog(gethtypes.Log{Address: parser.Addresses().VirtualsLaunchpad, Topics: []common.Hash{TopicVirtualsLaunched, addressTopic(token), addressTopic(venue)}, Data: data, BlockTimestamp: 1234})
	if err != nil || !ok {
		t.Fatalf("Virtuals Launched event=%#v ok=%v err=%v", event, ok, err)
	}
	launch := event.Data.(Launch)
	if launch.PreLaunch || launch.AntiSniperTaxType != 3 || !launch.IsProject60Days || launch.Venue != venue {
		t.Fatalf("unexpected Virtuals Launched: %#v", launch)
	}
	taxData, err := abiArguments(t, "uint256", "uint256", "uint256", "uint256").Pack(big.NewInt(100), big.NewInt(9900), big.NewInt(100), big.NewInt(250))
	if err != nil {
		t.Fatal(err)
	}
	event, ok, err = parser.ParseLog(gethtypes.Log{Address: token, Topics: []common.Hash{TopicVirtualsTax}, Data: taxData, BlockNumber: 99})
	if err != nil || !ok {
		t.Fatalf("Virtuals tax event=%#v ok=%v err=%v", event, ok, err)
	}
	policy := event.Data.(FeePolicy)
	if policy.Token != token || policy.BuyFeeBPS != 9900 || policy.SellFeeBPS != 250 || policy.EffectiveAt != 99 {
		t.Fatalf("unexpected Virtuals tax policy: %#v", policy)
	}
}

func TestParseVirtualsReceiptCapturesMintBeforePreLaunch(t *testing.T) {
	parser := New()
	token := common.HexToAddress("0x5000000000000000000000000000000000000005")
	pair := common.HexToAddress("0x6000000000000000000000000000000000000006")
	addressTopic := func(value common.Address) common.Hash {
		return common.BytesToHash(common.LeftPadBytes(value.Bytes(), 32))
	}
	pairData := make([]byte, 64)
	copy(pairData[12:32], pair.Bytes())
	big.NewInt(1).FillBytes(pairData[32:64])
	mintData := make([]byte, 64)
	big.NewInt(900).FillBytes(mintData[:32])
	big.NewInt(300).FillBytes(mintData[32:64])
	launchData := make([]byte, 7*32)
	big.NewInt(1).FillBytes(launchData[:32])
	// PreLaunched has token/pair indexed; virtualId, initialPurchase and the
	// five-word LaunchParams tuple are carried in data.
	receipt := &gethtypes.Receipt{Logs: []*gethtypes.Log{
		{Address: parser.Addresses().VirtualsFactory, Topics: []common.Hash{TopicVirtualsPairMade, addressTopic(token), addressTopic(parser.Addresses().VirtualToken)}, Data: pairData, Index: 1},
		{Address: pair, Topics: []common.Hash{TopicVirtualsPairMint}, Data: mintData, Index: 2},
		{Address: parser.Addresses().VirtualsLaunchpad, Topics: []common.Hash{TopicVirtualsPreLaunch, addressTopic(token), addressTopic(pair)}, Data: launchData, Index: 3},
	}}
	events, err := parser.ParseReceipt(receipt)
	if err != nil {
		t.Fatal(err)
	}
	var stateFound bool
	for _, event := range events {
		if event.Kind == EventVenueState {
			stateFound = true
		}
	}
	if !stateFound {
		t.Fatalf("Mint before PreLaunched was not captured: %#v", events)
	}
}

func TestParseO1FeeConfiguration(t *testing.T) {
	parser := New()
	data, err := abiArguments(t, "uint16", "uint16", "uint32").Pack(uint16(100), uint16(9_900), uint32(120))
	if err != nil {
		t.Fatal(err)
	}
	log := gethtypes.Log{
		Address: parser.Addresses().O1Factory,
		Topics:  []common.Hash{TopicO1FeeConfig, common.BigToHash(big.NewInt(7))},
		Data:    data, BlockNumber: 123,
	}
	event, ok, err := parser.ParseLog(log)
	if err != nil || !ok || event.Kind != EventFeePolicy || event.Protocol != ProtocolO1 {
		t.Fatalf("o1 fee event=%#v ok=%v err=%v", event, ok, err)
	}
	policy := event.Data.(FeePolicy)
	if policy.BaseFeeBPS != 100 || policy.AntiSnipeStartTotalBPS != 9_900 || policy.AntiSnipeWindowSeconds != 120 || policy.EffectiveAt != 123 {
		t.Fatalf("unexpected o1 fee policy: %#v", policy)
	}
	zeroBase, err := abiArguments(t, "uint16", "uint16", "uint32").Pack(uint16(0), uint16(300), uint32(120))
	if err != nil {
		t.Fatal(err)
	}
	log.Data = zeroBase
	if event, ok, err := parser.ParseLog(log); err != nil || !ok || event.Data.(FeePolicy).BaseFeeBPS != 0 {
		t.Fatalf("valid zero-base o1 fee rejected: event=%#v ok=%v err=%v", event, ok, err)
	}
	log.Data = data[:64]
	if _, ok, err := parser.ParseLog(log); err == nil || ok {
		t.Fatalf("truncated o1 fee event accepted: ok=%v err=%v", ok, err)
	}
	invalid, err := abiArguments(t, "uint16", "uint16", "uint32").Pack(uint16(1_001), uint16(9_900), uint32(120))
	if err != nil {
		t.Fatal(err)
	}
	log.Data = invalid
	if _, ok, err := parser.ParseLog(log); err == nil || ok {
		t.Fatalf("invalid o1 fee event accepted: ok=%v err=%v", ok, err)
	}
}

func TestLongTopicFromWrongEmitterIsIgnored(t *testing.T) {
	log := gethtypes.Log{Address: common.HexToAddress("0xdead"), Topics: []common.Hash{TopicLongLaunch}}
	if _, ok, err := New().ParseLog(log); err != nil || ok {
		t.Fatalf("wrong emitter accepted: ok=%v err=%v", ok, err)
	}
}

func TestPonsCurveRequiresRegisteredEmitter(t *testing.T) {
	parser := New()
	curve := common.HexToAddress("0x30")
	token := common.HexToAddress("0x31")
	actor := common.HexToAddress("0x32")
	recipient := common.HexToAddress("0x33")
	data, err := abiArguments(t, "uint256", "uint256", "uint256", "uint256").Pack(big.NewInt(100), big.NewInt(90), big.NewInt(5), big.NewInt(5))
	if err != nil {
		t.Fatal(err)
	}
	log := gethtypes.Log{Address: curve, Topics: []common.Hash{TopicPonsCurveBuy, addressTopic(actor), addressTopic(recipient)}, Data: data}
	if _, ok, err := parser.ParseLog(log); err != nil || ok {
		t.Fatalf("unregistered curve accepted: ok=%v err=%v", ok, err)
	}
	if err := parser.Registry().RegisterCurve(CurveRegistration{Curve: curve, Protocol: ProtocolPonsV2, Token: token, Quote: common.Address{}}); err != nil {
		t.Fatal(err)
	}
	event, ok, err := parser.ParseLog(log)
	if err != nil || !ok || event.Kind != EventCurveBuy {
		t.Fatalf("registered curve not parsed: %#v ok=%v err=%v", event, ok, err)
	}
}

func TestMalformedInitializeRejected(t *testing.T) {
	log := gethtypes.Log{Address: DefaultAddressBook().PoolManager, Topics: []common.Hash{TopicPoolInitialize}, Data: make([]byte, 160)}
	if _, _, err := New().ParseLog(log); err == nil {
		t.Fatal("malformed Initialize accepted")
	}
}

func TestRegistryConcurrentReadsAndIdempotentWrite(t *testing.T) {
	registry := NewRegistry()
	key := PoolKey{Currency0: common.Address{}, Currency1: common.HexToAddress("0x40"), Fee: 3000, TickSpacing: 60}
	id, err := PoolID(key)
	if err != nil {
		t.Fatal(err)
	}
	registration := PoolRegistration{PoolID: id, Protocol: ProtocolPoolsTrade, Token: key.Currency1, Quote: key.Currency0, PoolKey: key}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 1000 {
				registry.LookupPool(id)
			}
		}()
	}
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := registry.RegisterPool(registration); err != nil {
				t.Errorf("idempotent registration: %v", err)
			}
		}()
	}
	wg.Wait()
}

func TestRegistrySnapshotRestoreIsValidatedAndDeterministic(t *testing.T) {
	registry := NewRegistry()
	curve := CurveRegistration{Curve: common.HexToAddress("0x22"), Protocol: ProtocolPonsV2, Token: common.HexToAddress("0x33")}
	if err := registry.RegisterCurve(curve); err != nil {
		t.Fatal(err)
	}
	key := PoolKey{Currency0: common.Address{}, Currency1: common.HexToAddress("0x44"), Fee: 3000, TickSpacing: 60}
	id, err := PoolID(key)
	if err != nil {
		t.Fatal(err)
	}
	pool := PoolRegistration{PoolID: id, Protocol: ProtocolPoolsTrade, Token: key.Currency1, Quote: key.Currency0, PoolKey: key}
	if err := registry.RegisterPool(pool); err != nil {
		t.Fatal(err)
	}
	snapshot := registry.Snapshot()
	restored, err := NewRegistryFromSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if got := restored.Snapshot(); !reflect.DeepEqual(got, snapshot) {
		t.Fatalf("restored snapshot differs: got=%#v want=%#v", got, snapshot)
	}
	bad := snapshot
	bad.Pools = append([]PoolRegistration(nil), snapshot.Pools...)
	bad.Pools[0].PoolID = common.HexToHash("0xdead")
	if err := registry.Restore(bad); !errors.Is(err, ErrPoolIDMismatch) {
		t.Fatalf("invalid restore error = %v", err)
	}
	if got := registry.Snapshot(); !reflect.DeepEqual(got, snapshot) {
		t.Fatal("failed restore changed live registry")
	}
}

func TestPendingPoolConnectsLaterInitializeAndSwap(t *testing.T) {
	parser := New()
	token := common.HexToAddress("0x50")
	quote := common.HexToAddress("0x40")
	key := PoolKey{Currency0: quote, Currency1: token, Fee: 0x800000, TickSpacing: 8, Hooks: DefaultAddressBook().BagsHook}
	id, err := PoolID(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := parser.Registry().RegisterPendingPool(PendingPoolRegistration{PoolID: id, Protocol: ProtocolBagsV2, Token: token, Quote: quote}); err != nil {
		t.Fatal(err)
	}
	initializeLog := makeInitializeLog(t, key, 1)
	swapLog := makeSwapLog(t, key, 2)
	events, err := parser.ParseReceipt(&gethtypes.Receipt{Logs: []*gethtypes.Log{&initializeLog, &swapLog}})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Protocol != ProtocolBagsV2 || events[1].Kind != EventSwap || events[1].Protocol != ProtocolBagsV2 {
		t.Fatalf("pending pool was not connected: %#v", events)
	}
}

func TestCanonicalBagsInitializeRegistersWithoutEarlierLaunch(t *testing.T) {
	parser := New()
	addresses := DefaultAddressBook()
	token := common.HexToAddress("0xf000000000000000000000000000000000000001")
	key := PoolKey{
		Currency0: addresses.WETH, Currency1: token,
		Fee: 1 << 23, TickSpacing: 60, Hooks: addresses.BagsHook,
	}
	id, err := PoolID(key)
	if err != nil {
		t.Fatal(err)
	}
	event, ok, err := parser.ParseLog(makeInitializeLog(t, key, 1))
	if err != nil || !ok || event.Protocol != ProtocolBagsV2 {
		t.Fatalf("parse canonical Bags initialize: event=%#v ok=%v err=%v", event, ok, err)
	}
	registration, ok := parser.Registry().LookupPool(id)
	if !ok || registration.Protocol != ProtocolBagsV2 || registration.Token != token || registration.Quote != addresses.WETH {
		t.Fatalf("canonical Bags pool was not registered: %#v ok=%v", registration, ok)
	}
}

func TestBagsInitializeInferenceRequiresExactCanonicalPoolKey(t *testing.T) {
	addresses := DefaultAddressBook()
	token := common.HexToAddress("0xf000000000000000000000000000000000000001")
	valid := PoolKey{Currency0: addresses.WETH, Currency1: token, Fee: 1 << 23, TickSpacing: 60, Hooks: addresses.BagsHook}
	tests := []struct {
		name string
		key  PoolKey
	}{
		{name: "wrong hook", key: func() PoolKey { key := valid; key.Hooks = common.HexToAddress("0x1"); return key }()},
		{name: "static fee", key: func() PoolKey { key := valid; key.Fee = 3_000; return key }()},
		{name: "wrong spacing", key: func() PoolKey { key := valid; key.TickSpacing = 8; return key }()},
		{name: "without WETH", key: PoolKey{Currency0: common.HexToAddress("0x1"), Currency1: token, Fee: 1 << 23, TickSpacing: 60, Hooks: addresses.BagsHook}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parser := New()
			event, ok, err := parser.ParseLog(makeInitializeLog(t, test.key, 1))
			if err != nil || !ok {
				t.Fatalf("parse initialize: ok=%v err=%v", ok, err)
			}
			if event.Protocol != ProtocolUnknown {
				t.Fatalf("protocol = %v, want unknown", event.Protocol)
			}
			id, err := PoolID(test.key)
			if err != nil {
				t.Fatal(err)
			}
			if _, registered := parser.Registry().LookupPool(id); registered {
				t.Fatal("non-canonical Bags pool was registered")
			}
		})
	}
}

func TestStreamingBagsLaunchRegistersAndClearsPendingPool(t *testing.T) {
	parser := New()
	token := common.HexToAddress("0xf000000000000000000000000000000000000001")
	quote := DefaultAddressBook().WETH
	curve := common.HexToAddress("0x70")
	key := PoolKey{Currency0: quote, Currency1: token, Fee: 0x800000, TickSpacing: 8, Hooks: DefaultAddressBook().BagsHook}
	id, err := PoolID(key)
	if err != nil {
		t.Fatal(err)
	}
	launchLog := makeBagsLaunchLog(t, token, curve, id, 1)
	event, ok, err := parser.ParseLog(launchLog)
	if err != nil || !ok || event.Protocol != ProtocolBagsV2 {
		t.Fatalf("parse launch: event=%#v ok=%v err=%v", event, ok, err)
	}
	if _, ok := parser.Registry().LookupCurve(curve); !ok {
		t.Fatal("curve was not registered")
	}
	if _, ok := parser.Registry().LookupPendingPool(id); !ok {
		t.Fatal("pending pool was not registered")
	}
	if _, ok, err = parser.ParseLog(makeInitializeLog(t, key, 2)); err != nil || !ok {
		t.Fatalf("parse initialize: ok=%v err=%v", ok, err)
	}
	if _, ok := parser.Registry().LookupPool(id); !ok {
		t.Fatal("pool was not registered")
	}
	if _, ok := parser.Registry().LookupPendingPool(id); ok {
		t.Fatal("registered pool remained pending")
	}
	if _, ok, err = parser.ParseLog(launchLog); err != nil || !ok {
		t.Fatalf("replay launch: ok=%v err=%v", ok, err)
	}
	if _, ok := parser.Registry().LookupPendingPool(id); ok {
		t.Fatal("launch replay recreated pending pool")
	}
}

func TestRegistryRejectsUnknownProtocol(t *testing.T) {
	registry := NewRegistry()
	err := registry.RegisterCurve(CurveRegistration{
		Curve:    common.HexToAddress("0x71"),
		Protocol: Protocol(255),
		Token:    common.HexToAddress("0x72"),
	})
	if err == nil {
		t.Fatal("unknown protocol was accepted")
	}
}

func TestRegistryConcurrentConflictHasSingleWinner(t *testing.T) {
	registry := NewRegistry()
	key := PoolKey{Currency0: common.Address{}, Currency1: common.HexToAddress("0x73"), Fee: 3000, TickSpacing: 60}
	id, err := PoolID(key)
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	for _, protocol := range []Protocol{ProtocolPonsV2, ProtocolPoolsTrade} {
		go func() {
			results <- registry.RegisterPool(PoolRegistration{PoolID: id, Protocol: protocol, Token: key.Currency1, Quote: key.Currency0, PoolKey: key})
		}()
	}
	first, second := <-results, <-results
	if (first == nil) == (second == nil) {
		t.Fatalf("want one winner and one conflict, got %v and %v", first, second)
	}
	if first != nil && !errors.Is(first, ErrConflictingPool) || second != nil && !errors.Is(second, ErrConflictingPool) {
		t.Fatalf("unexpected conflict errors: %v and %v", first, second)
	}
	if len(registry.Pools()) != 1 {
		t.Fatalf("pool count = %d, want 1", len(registry.Pools()))
	}
}

func TestStreamingPonsLaunchRegistersCurve(t *testing.T) {
	parser := New()
	token := common.HexToAddress("0x81")
	curve := common.HexToAddress("0x82")
	creator := common.HexToAddress("0x83")
	data, err := abiArguments(t, "address", "uint256", "uint256").Pack(common.Address{}, big.NewInt(2), big.NewInt(3))
	if err != nil {
		t.Fatal(err)
	}
	log := gethtypes.Log{Address: DefaultAddressBook().PonsFactory, Topics: []common.Hash{TopicPonsLaunch, addressTopic(token), addressTopic(curve), addressTopic(creator)}, Data: data}
	event, ok, err := parser.ParseLog(log)
	if err != nil || !ok || event.Protocol != ProtocolPonsV2 {
		t.Fatalf("parse launch: event=%#v ok=%v err=%v", event, ok, err)
	}
	registration, ok := parser.Registry().LookupCurve(curve)
	if !ok || registration.Token != token || registration.Protocol != ProtocolPonsV2 {
		t.Fatalf("curve registration = %#v, ok=%v", registration, ok)
	}
}

func TestPonsLaunchRejectsZeroCurve(t *testing.T) {
	data, err := abiArguments(t, "address", "uint256", "uint256").Pack(common.Address{}, new(big.Int), big.NewInt(1))
	if err != nil {
		t.Fatal(err)
	}
	log := gethtypes.Log{
		Address: DefaultAddressBook().PonsFactory,
		Topics: []common.Hash{
			TopicPonsLaunch,
			addressTopic(common.HexToAddress("0x84")),
			common.Hash{},
			addressTopic(common.HexToAddress("0x85")),
		},
		Data: data,
	}
	if _, _, err := New().ParseLog(log); !errors.Is(err, ErrMalformedLog) {
		t.Fatalf("error = %v, want ErrMalformedLog", err)
	}
}

func TestMalformedBagsLaunchDoesNotPolluteRegistry(t *testing.T) {
	parser := New()
	token := common.HexToAddress("0x91")
	curve := common.HexToAddress("0x92")
	poolID := common.HexToHash("0x93")
	log := makeBagsLaunchLog(t, token, curve, poolID, 1)
	for i := 96; i < 128; i++ {
		log.Data[i] = 0xff
	}
	if _, _, err := parser.ParseLog(log); err == nil {
		t.Fatal("malformed dynamic Bags data accepted")
	}
	if _, ok := parser.Registry().LookupCurve(curve); ok {
		t.Fatal("malformed launch registered curve")
	}
	if _, ok := parser.Registry().LookupPendingPool(poolID); ok {
		t.Fatal("malformed launch registered pending pool")
	}
}

func TestConflictingBagsLaunchDoesNotPartiallyRegisterCurve(t *testing.T) {
	parser := New()
	token := common.HexToAddress("0xa1")
	curve := common.HexToAddress("0xa2")
	poolID := common.HexToHash("0xa3")
	if err := parser.Registry().RegisterPendingPool(PendingPoolRegistration{PoolID: poolID, Protocol: ProtocolPAIR, Token: token}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := parser.ParseLog(makeBagsLaunchLog(t, token, curve, poolID, 1)); !errors.Is(err, ErrConflictingPool) {
		t.Fatalf("error = %v, want ErrConflictingPool", err)
	}
	if _, ok := parser.Registry().LookupCurve(curve); ok {
		t.Fatal("failed launch partially registered curve")
	}
}

func TestFailedReceiptDoesNotPolluteRegistry(t *testing.T) {
	parser := New()
	token := common.HexToAddress("0xb1")
	curve := common.HexToAddress("0xb2")
	creator := common.HexToAddress("0xb3")
	data, err := abiArguments(t, "address", "uint256", "uint256").Pack(common.Address{}, big.NewInt(1), big.NewInt(2))
	if err != nil {
		t.Fatal(err)
	}
	launch := gethtypes.Log{Address: DefaultAddressBook().PonsFactory, Topics: []common.Hash{TopicPonsLaunch, addressTopic(token), addressTopic(curve), addressTopic(creator)}, Data: data, Index: 1}
	malformedInitialize := gethtypes.Log{Address: DefaultAddressBook().PoolManager, Topics: []common.Hash{TopicPoolInitialize}, Index: 2}
	if _, err := parser.ParseReceipt(&gethtypes.Receipt{Logs: []*gethtypes.Log{&launch, &malformedInitialize}}); err == nil {
		t.Fatal("malformed receipt was accepted")
	}
	if _, ok := parser.Registry().LookupCurve(curve); ok {
		t.Fatal("failed receipt polluted curve registry")
	}
}

func TestConcurrentPendingAndPoolConflictLeavesConsistentWinner(t *testing.T) {
	registry := NewRegistry()
	key := PoolKey{Currency0: common.Address{}, Currency1: common.HexToAddress("0xc1"), Fee: 3000, TickSpacing: 60}
	id, err := PoolID(key)
	if err != nil {
		t.Fatal(err)
	}
	pool := PoolRegistration{PoolID: id, Protocol: ProtocolPoolsTrade, Token: key.Currency1, Quote: key.Currency0, PoolKey: key}
	pending := PendingPoolRegistration{PoolID: id, Protocol: ProtocolPAIR, Token: key.Currency1, Quote: key.Currency0}
	results := make(chan error, 2)
	go func() { results <- registry.RegisterPool(pool) }()
	go func() { results <- registry.RegisterPendingPool(pending) }()
	first, second := <-results, <-results
	if (first == nil) == (second == nil) {
		t.Fatalf("want one winner and one conflict, got %v and %v", first, second)
	}
	_, hasPool := registry.LookupPool(id)
	_, hasPending := registry.LookupPendingPool(id)
	if hasPool == hasPending {
		t.Fatalf("inconsistent state: pool=%v pending=%v", hasPool, hasPending)
	}
}

func TestRealBagsCurveTradeFixtures(t *testing.T) {
	const actor = "0x9689992f5b5c09447f15906d8d11214944488341"
	curve := common.HexToAddress("0x0c68fe19e53a72986fa9663cadbc6334199efebc")
	parser := New()
	if err := parser.Registry().RegisterCurve(CurveRegistration{Curve: curve, Protocol: ProtocolBagsV2, Token: common.HexToAddress("0xdb89f57b36e2778d1c7a42f8cda4a5bf496e76ae"), Quote: DefaultAddressBook().WETH}); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name      string
		topic     common.Hash
		data      string
		kind      EventKind
		amountIn  string
		amountOut string
		fee       string
	}{
		{"buy", TopicBagsCurveBuy, "0x00000000000000000000000000000000000000000000000000232bff5f46c000000000000000000000000000000000000000000000000000002277eae79c60000000000000000000000000000000000000000000000674a4f98dc41feaba987b0000000000000000000000000000000000000000000000000000b41477aa600000000000000000000000000000000000000000000000000000005a0a3bd5300000000000000000000000000000000000000000000000000000005a0a3bd530000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000004aa7cd6100000000000000000000000000000000000000000358f20950a2170fd4b202ac0000000000000000000000000000000000000000000000001201eeda08e2fb2d", EventCurveBuy, "232bff5f46c000", "674a4f98dc41feaba987b", "b41477aa6000"},
		{"sell", TopicBagsCurveSell, "0x0000000000000000000000000000000000000000000674a4f98dc41feaba987b000000000000000000000000000000000000000000000000002277eae79c5fff0000000000000000000000000000000000000000000000000021c7707256b0000000000000000000000000000000000000000000000000000000b07a7545afff0000000000000000000000000000000000000000000000000000583d3aa2d7ff0000000000000000000000000000000000000000000000000000583d3aa2d80000000000000000000000000000000000000000000000000000000000498b12ba0000000000000000000000000000000000000000035f66ae4a2fdb2fbf6c9b2700000000000000000000000000000000000000000000000011df76ef21469b2e", EventCurveSell, "674a4f98dc41feaba987b", "21c7707256b000", "b07a7545afff"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			log := gethtypes.Log{Address: curve, Topics: []common.Hash{test.topic, addressTopic(common.HexToAddress(actor)), addressTopic(common.HexToAddress(actor))}, Data: common.FromHex(test.data)}
			event, ok, err := parser.ParseLog(log)
			if err != nil || !ok || event.Kind != test.kind {
				t.Fatalf("parse fixture: event=%#v ok=%v err=%v", event, ok, err)
			}
			trade := event.Data.(CurveTrade)
			if trade.AmountIn.Text(16) != test.amountIn || trade.AmountOut.Text(16) != test.amountOut || trade.Fee.Text(16) != test.fee {
				t.Fatalf("unexpected trade: %#v", trade)
			}
		})
	}
}

func TestKnownTopics(t *testing.T) {
	tests := map[string]common.Hash{
		"Initialize": topicPoolInitialize, "Swap": topicPoolSwap,
		"Pons launch": topicPonsLaunch, "Pons buy": topicPonsCurveBuy, "Pons sell": topicPonsCurveSell, "Pons pool": topicPonsPool,
		"Long": topicLongLaunch, "o1": topicO1Launch, "Pools": topicPoolsCreated, "PAIR": topicPAIRPool,
		"Bags launch": topicBagsCreated, "Bags buy": topicBagsCurveBuy, "Bags sell": topicBagsCurveSell,
	}
	want := map[string]string{
		"Initialize":  "0xdd466e674ea557f56295e2d0218a125ea4b4f0f6f3307b95f85e6110838d6438",
		"Swap":        "0x40e9cecb9f5f1f1c5b9c97dec2917b7ee92e57ba5563708daca94dd84ad7112f",
		"Pons launch": "0x8d4aad4953d0ca700d468f3753aa14432d1b35b43ec6409f051fb6aa43a89607",
		"Pons buy":    "0xec36bf571f136799e8dc0b0b8bea4b04d8bd3d43de838aab0d5fc21d4cbfc455",
		"Pons sell":   "0x8113d738abdcb6b38357e9d53a54a7157861a09031b453651f0fe7fe151f59df",
		"Pons pool":   "0x01bf263a1db1652580721573296e1a1fa70b3d4c87f61d02a69c4e1109d2d573",
		"Long":        "0xadc6f1f726f7c710f77ec06adc75f3bb964e5be19581b072c67f7b9b4039267b",
		"o1":          "0x207384e895174175cc774fe7f7457b37c382f27ebf53d37d5257b862f80eaf9c",
		"Pools":       "0x2e2b3f61b70d2d131b2a807371103cc98d51adcaa5e9a8f9c32658ad8426e74e",
		"PAIR":        "0x02f2fb2145fe2cb08b684bb8dbeed1ad361e424c5730fe9470e02b83f0890ea3",
		"Bags launch": "0x643b3b606052cbadac2f906ad0b462da99eda2a1d4f824d315d7f6edd3e4cced",
		"Bags buy":    "0x6d9c6fad0db13f6f7fca7124777996deaeb1949d0750a4874c18611ff5d436b9",
		"Bags sell":   "0x813ea2e4b7710a4562c34494d61d6e80fd2ba5105790f740fdcbf64f8b05b80d",
	}
	for name, topic := range tests {
		if topic.Hex() != want[name] {
			t.Fatalf("%s topic = %s", name, topic.Hex())
		}
	}
}

func TestKnownPonsMemeHookAddress(t *testing.T) {
	want := common.HexToAddress("0xE5e702641Ea86F4ae6cC3cDaeD2B886f976Be044")
	if got := DefaultAddressBook().PonsMemeHook; got != want {
		t.Fatalf("PonsMemeHook = %s, want %s", got, want)
	}
}

func FuzzParseLogNoPanic(f *testing.F) {
	f.Add([]byte{1, 2, 3}, uint8(2))
	f.Fuzz(func(t *testing.T, data []byte, topicCount uint8) {
		if len(data) > maxLogDataBytes+1 {
			data = data[:maxLogDataBytes+1]
		}
		count := int(topicCount % 8)
		topics := make([]common.Hash, count)
		if count > 0 {
			topics[0] = TopicPoolInitialize
		}
		_, _, _ = New().ParseLog(gethtypes.Log{Address: DefaultAddressBook().PoolManager, Topics: topics, Data: data})
	})
}

func TestNilParserPublicAPIsDoNotPanic(t *testing.T) {
	var parser *Parser
	if parser.Registry() != nil {
		t.Fatal("nil parser returned a registry")
	}
	if parser.Addresses() != (AddressBook{}) {
		t.Fatal("nil parser returned addresses")
	}
	if _, ok, err := parser.ParseLog(gethtypes.Log{}); ok || err != nil {
		t.Fatalf("ParseLog: ok=%v err=%v", ok, err)
	}
	if _, err := parser.ParseReceipt(&gethtypes.Receipt{}); !errors.Is(err, ErrInvalidRegistration) {
		t.Fatalf("ParseReceipt error=%v, want ErrInvalidRegistration", err)
	}
}

func BenchmarkParseRegisteredSwap(b *testing.B) {
	parser := New()
	key := PoolKey{Currency0: common.Address{}, Currency1: common.HexToAddress("0x40"), Fee: 3000, TickSpacing: 60}
	id, _ := PoolID(key)
	_ = parser.Registry().RegisterPool(PoolRegistration{PoolID: id, Protocol: ProtocolPoolsTrade, Token: key.Currency1, Quote: key.Currency0, PoolKey: key})
	log := makeSwapLog(b, key, 1)
	b.ReportAllocs()
	for b.Loop() {
		if _, ok, err := parser.ParseLog(log); err != nil || !ok {
			b.Fatalf("parse failed: ok=%v err=%v", ok, err)
		}
	}
}
