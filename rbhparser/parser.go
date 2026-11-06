package rbhparser

import (
	"fmt"
	"sort"

	"github.com/ethereum/go-ethereum/common"
	gethtypes "github.com/ethereum/go-ethereum/core/types"
)

type Parser struct {
	addresses AddressBook
	registry  *Registry
}

// LogErrorHandler may accept a single malformed receipt log so parsing can
// continue. Callers should only accept errors for events they can safely drop.
type LogErrorHandler func(log gethtypes.Log, err error) bool

func New() *Parser {
	return NewWithRegistry(DefaultAddressBook(), NewRegistry())
}

func NewWithRegistry(addresses AddressBook, registry *Registry) *Parser {
	if registry == nil {
		registry = NewRegistry()
	}
	return &Parser{addresses: addresses, registry: registry}
}

func (p *Parser) Registry() *Registry {
	if p == nil {
		return nil
	}
	return p.registry
}

func (p *Parser) Addresses() AddressBook {
	if p == nil {
		return AddressBook{}
	}
	return p.addresses
}

func (p *Parser) ParseLog(log gethtypes.Log) (Event, bool, error) {
	if p == nil || p.registry == nil || log.Removed || len(log.Topics) == 0 {
		return Event{}, false, nil
	}
	topic := log.Topics[0]
	if topic == topicProtocolUpgraded {
		protocol, known := p.upgradeProtocol(log.Address)
		if !known || len(log.Topics) != 2 || len(log.Data) != 0 {
			return Event{}, false, nil
		}
		implementation, err := topicAddress(log.Topics[1])
		if err != nil || implementation == (common.Address{}) {
			return Event{}, false, malformed("protocol upgrade", ErrMalformedLog)
		}
		return Event{Kind: EventProtocolUpgrade, Protocol: protocol, Data: ProtocolUpgrade{Proxy: log.Address, Implementation: implementation}, Log: log}, true, nil
	}
	if log.Address == p.addresses.PoolManager {
		switch topic {
		case topicPoolInitialize:
			initialized, err := decodeInitialize(log)
			if err != nil {
				return Event{}, false, malformed("Initialize", err)
			}
			protocol := ProtocolUnknown
			if registration, ok := p.registry.LookupPool(initialized.PoolID); ok {
				protocol = registration.Protocol
			} else if pending, ok := p.registry.LookupPendingPool(initialized.PoolID); ok {
				registration, registrationErr := poolRegistrationFromPending(pending, initialized)
				if registrationErr != nil {
					return Event{}, false, registrationErr
				}
				if registrationErr = p.registry.RegisterPool(registration); registrationErr != nil {
					return Event{}, false, registrationErr
				}
				protocol = pending.Protocol
			} else if registration, ok := canonicalBagsPoolRegistration(initialized, p.addresses); ok {
				if registrationErr := p.registry.RegisterPool(registration); registrationErr != nil {
					return Event{}, false, registrationErr
				}
				protocol = ProtocolBagsV2
			} else if registration, ok := canonicalPonsUSDGPoolRegistration(initialized, p.addresses); ok {
				if registrationErr := p.registry.RegisterPool(registration); registrationErr != nil {
					return Event{}, false, registrationErr
				}
				protocol = ProtocolPonsV2
			}
			return Event{Kind: EventPoolInitialized, Protocol: protocol, Data: initialized, Log: log}, true, nil
		case topicPoolSwap:
			if len(log.Topics) < 2 {
				return Event{}, false, malformed("Swap", ErrMalformedLog)
			}
			registration, ok := p.registry.LookupPool(log.Topics[1])
			if !ok {
				return Event{}, false, nil
			}
			swap, err := decodeSwap(log, registration)
			if err != nil {
				return Event{}, false, malformed("Swap", err)
			}
			return Event{Kind: EventSwap, Protocol: registration.Protocol, Data: swap, Log: log}, true, nil
		case topicPoolLiquidity:
			modified, err := decodeLiquidityModified(log)
			if err != nil {
				return Event{}, false, malformed("ModifyLiquidity", err)
			}
			protocol := ProtocolUnknown
			if registration, ok := p.registry.LookupPool(modified.PoolID); ok {
				protocol = registration.Protocol
			}
			return Event{Kind: EventLiquidityModified, Protocol: protocol, Data: modified, Log: log}, true, nil
		}
	}

	if log.Address == p.addresses.PonsFactory && topic == topicPonsLaunch {
		launch, err := decodePonsLaunch(log)
		return p.launchEvent(log, ProtocolPonsV2, launch, err)
	}
	if log.Address == p.addresses.PonsMemeHook && topic == topicPonsPool {
		launch, err := decodePonsPool(log)
		return p.launchEvent(log, ProtocolPonsV2, launch, err)
	}
	if log.Address == p.addresses.LongLauncher && topic == topicLongLaunch {
		launch, err := decodeLongLaunch(log)
		return p.launchEvent(log, ProtocolLong, launch, err)
	}
	if log.Address == p.addresses.O1Factory && topic == topicO1Launch {
		launch, err := decodeO1Launch(log)
		return p.launchEvent(log, ProtocolO1, launch, err)
	}
	if log.Address == p.addresses.O1Factory && topic == topicO1FeeConfig {
		policy, err := decodeO1FeePolicy(log)
		if err != nil {
			return Event{}, false, malformed("o1 fee configuration", err)
		}
		return Event{Kind: EventFeePolicy, Protocol: ProtocolO1, Data: policy, Log: log}, true, nil
	}
	if log.Address == p.addresses.PoolsEntry && topic == topicPoolsCreated {
		launch, err := decodePoolsLaunch(log)
		return p.launchEvent(log, ProtocolPoolsTrade, launch, err)
	}
	if log.Address == p.addresses.PAIRLaunchpad && topic == topicPAIRPool {
		launch, err := decodePAIRLaunch(log)
		return p.launchEvent(log, ProtocolPAIR, launch, err)
	}
	if log.Address == p.addresses.BagsFactory && topic == topicBagsCreated {
		launch, err := decodeBagsLaunch(log, p.addresses.WETH)
		return p.launchEvent(log, ProtocolBagsV2, launch, err)
	}
	if log.Address == p.addresses.LetsCashFactory && topic == topicLetsCashLaunch {
		launch, err := decodeLetsCashLaunch(log)
		return p.launchEvent(log, ProtocolLetsCash, launch, err)
	}
	if log.Address == p.addresses.LetsCashHook && topic == topicLetsCashFee {
		policy, err := decodeLetsCashFeePolicy(log)
		if err != nil {
			return Event{}, false, malformed("letscash fee policy", err)
		}
		return Event{Kind: EventFeePolicy, Protocol: ProtocolLetsCash, Data: policy, Log: log}, true, nil
	}
	if log.Address == p.addresses.FlapController && (topic == topicFlapTax || topic == topicFlapAsymmetricTax) {
		token, err := addressWord(log.Data, 0)
		if err != nil {
			return Event{}, false, malformed("Flap fee policy", err)
		}
		protocol := ProtocolUnknown
		if registration, ok := p.registry.LookupToken(token); ok {
			protocol = registration.Protocol
		}
		if protocol == ProtocolUnknown {
			protocol = ProtocolFlapTax
		}
		policy, err := decodeFlapTaxPolicy(log, topic == topicFlapAsymmetricTax)
		if err != nil {
			return Event{}, false, malformed("Flap fee policy", err)
		}
		return Event{Kind: EventFeePolicy, Protocol: protocol, Data: policy, Log: log}, true, nil
	}
	if log.Address == p.addresses.FlapController && topic == topicFlapSupply {
		token, supply, err := decodeFlapTokenAndUint(log, 256)
		if err != nil {
			return Event{}, false, malformed("Flap circulating supply", err)
		}
		registration, ok := p.registry.LookupToken(token)
		if !ok || (registration.Protocol != ProtocolFlapTax && registration.Protocol != ProtocolFlapStocks) {
			return Event{}, false, nil
		}
		return Event{Kind: EventFlapSupply, Protocol: registration.Protocol, Data: FlapSupply{Token: token, CirculatingSupply: supply}, Log: log}, true, nil
	}
	if log.Address == p.addresses.FlapController && topic == topicFlapGraduated {
		graduation, err := decodeFlapGraduation(log)
		if err != nil {
			return Event{}, false, malformed("Flap graduation", err)
		}
		registration, ok := p.registry.LookupToken(graduation.Token)
		if !ok || (registration.Protocol != ProtocolFlapTax && registration.Protocol != ProtocolFlapStocks) {
			return Event{}, false, nil
		}
		if err := p.registry.RegisterVenue(TokenRegistration{Token: registration.Token, Protocol: registration.Protocol, Quote: registration.Quote, Venue: graduation.Venue}); err != nil {
			return Event{}, false, err
		}
		return Event{Kind: EventGraduation, Protocol: registration.Protocol, Data: graduation, Log: log}, true, nil
	}
	if topic == topicFlapPoolState {
		registration, ok := p.registry.LookupToken(log.Address)
		if !ok || registration.Protocol != ProtocolFlapTax {
			return Event{}, false, nil
		}
		if err := validateLog(log, 1, 64); err != nil {
			return Event{}, false, malformed("Flap pool state", err)
		}
		state, err := uintWord(log.Data, 1, 8)
		if err != nil || !state.IsUint64() || state.Uint64() != uint64(flapTaxFreeState) {
			return Event{}, false, nil
		}
		policy := FeePolicy{Token: log.Address, Denominator: 10_000}
		return Event{Kind: EventFeePolicy, Protocol: ProtocolFlapTax, Data: policy, Log: log}, true, nil
	}
	if log.Address == p.addresses.VaroLaunchpad && topic == topicVaroLaunch {
		launch, err := decodeVaroLaunch(log)
		return p.launchEvent(log, ProtocolVaro, launch, err)
	}
	if log.Address == p.addresses.VaroLaunchpad && topic == topicVaroFee {
		if len(log.Topics) != 3 {
			return Event{}, false, malformed("Varo fee policy", ErrMalformedLog)
		}
		token, err := topicAddress(log.Topics[1])
		if err != nil {
			return Event{}, false, malformed("Varo fee policy", err)
		}
		registration, ok := p.registry.LookupToken(token)
		if !ok || registration.Protocol != ProtocolVaro {
			return Event{}, false, nil
		}
		policy, err := decodeVaroFeePolicy(log, registration)
		if err != nil {
			return Event{}, false, malformed("Varo fee policy", err)
		}
		return Event{Kind: EventFeePolicy, Protocol: ProtocolVaro, Data: policy, Log: log}, true, nil
	}
	if log.Address == p.addresses.VirtualsLaunchpad && (topic == topicVirtualsPreLaunch || topic == topicVirtualsLaunched) {
		launch, err := decodeVirtualsLaunch(log, p.addresses.VirtualToken, topic == topicVirtualsLaunched)
		return p.launchEvent(log, ProtocolVirtuals, launch, err)
	}
	if log.Address == p.addresses.VirtualsFactory && topic == topicVirtualsPairMade {
		if len(log.Topics) != 3 || len(log.Data) != 64 {
			return Event{}, false, malformed("Virtuals pair creation", ErrMalformedLog)
		}
		token, err := topicAddress(log.Topics[1])
		if err != nil || token == (common.Address{}) {
			return Event{}, false, malformed("Virtuals pair creation", ErrMalformedLog)
		}
		quote, err := topicAddress(log.Topics[2])
		if err != nil || quote != p.addresses.VirtualToken {
			return Event{}, false, malformed("Virtuals pair creation", ErrMalformedLog)
		}
		venue, err := addressWord(log.Data, 0)
		if err != nil || venue == (common.Address{}) || venue == token || venue == quote {
			return Event{}, false, malformed("Virtuals pair creation", ErrMalformedLog)
		}
		if err := p.registry.RegisterToken(TokenRegistration{Token: token, Protocol: ProtocolVirtuals, Quote: quote, Venue: venue}); err != nil {
			return Event{}, false, err
		}
		return Event{}, false, nil
	}
	if log.Address == p.addresses.VirtualsLaunchpad && topic == topicVirtualsGraduated {
		if len(log.Topics) != 2 || len(log.Data) != 32 {
			return Event{}, false, malformed("Virtuals graduation", ErrMalformedLog)
		}
		token, err := topicAddress(log.Topics[1])
		if err != nil {
			return Event{}, false, malformed("Virtuals graduation", err)
		}
		agentToken, err := addressWord(log.Data, 0)
		if err != nil || agentToken == (common.Address{}) {
			return Event{}, false, malformed("Virtuals graduation", ErrMalformedLog)
		}
		return Event{Kind: EventGraduation, Protocol: ProtocolVirtuals, Data: Graduation{Token: token, AgentToken: agentToken}, Log: log}, true, nil
	}
	if topic == topicVirtualsTax {
		registration, ok := p.registry.LookupToken(log.Address)
		if !ok || registration.Protocol != ProtocolVirtuals {
			return Event{}, false, nil
		}
		policy, err := decodeVirtualsFeePolicy(log)
		if err != nil {
			return Event{}, false, malformed("Virtuals fee policy", err)
		}
		return Event{Kind: EventFeePolicy, Protocol: ProtocolVirtuals, Data: policy, Log: log}, true, nil
	}
	if log.Address == p.addresses.WETHVirtualPair && topic == topicV2PairSync {
		state, err := decodeVirtualsAbsoluteState(log, 112)
		if err != nil {
			return Event{}, false, malformed("Virtuals WETH/VIRTUAL Sync", err)
		}
		return Event{Kind: EventVenueState, Protocol: ProtocolVirtuals, Data: state, Log: log}, true, nil
	}
	if registration, ok := p.registry.LookupVenue(log.Address); ok && registration.Protocol == ProtocolVirtuals {
		var state VenueStateChange
		var err error
		switch topic {
		case topicVirtualsPairMint, topicVirtualsPairSync:
			state, err = decodeVirtualsAbsoluteState(log, 256)
		default:
			break
		}
		if topic == topicVirtualsPairMint || topic == topicVirtualsPairSync {
			if err != nil {
				return Event{}, false, malformed("Virtuals pair state", err)
			}
			return Event{Kind: EventVenueState, Protocol: ProtocolVirtuals, Data: state, Log: log}, true, nil
		}
	}
	if registration, ok := p.registry.LookupVenue(log.Address); ok && (registration.Protocol == ProtocolFlapTax || registration.Protocol == ProtocolFlapStocks) {
		switch topic {
		case topicV2PairSync:
			state, err := decodeVirtualsAbsoluteState(log, 112)
			if err != nil {
				return Event{}, false, malformed("Flap V2 pair state", err)
			}
			state.Token = registration.Token
			return Event{Kind: EventVenueState, Protocol: registration.Protocol, Data: state, Log: log}, true, nil
		case topicVirtualsPairSwap:
			swap, err := decodeFlapV2Swap(log, registration, p.addresses.WETH)
			if err != nil {
				return Event{}, false, malformed("Flap V2 swap", err)
			}
			return Event{Kind: EventVenueSwap, Protocol: registration.Protocol, Data: swap, Log: log}, true, nil
		}
	}
	if log.Address == p.addresses.VaroRouter && topic == topicVaroBuy {
		if len(log.Topics) < 3 {
			return Event{}, false, malformed("Varo buy", ErrMalformedLog)
		}
		token, err := topicAddress(log.Topics[2])
		if err != nil {
			return Event{}, false, malformed("Varo buy", err)
		}
		registration, ok := p.registry.LookupToken(token)
		if !ok || registration.Protocol != ProtocolVaro {
			return Event{}, false, nil
		}
		swap, err := decodeVaroBuy(log, registration)
		if err != nil {
			return Event{}, false, malformed("Varo buy", err)
		}
		return Event{Kind: EventVenueSwap, Protocol: ProtocolVaro, Data: swap, Log: log}, true, nil
	}
	if log.Address == p.addresses.GMGNRouter && topic == topicGMGNSwap {
		swap, protocol, ok, err := p.decodeGMGNSwap(log)
		if err != nil {
			return Event{}, false, malformed("GMGN swap", err)
		}
		if !ok {
			return Event{}, false, nil
		}
		return Event{Kind: EventVenueSwap, Protocol: protocol, Data: swap, Log: log}, true, nil
	}
	if topic == topicVirtualsBuy {
		registration, ok := p.registry.LookupVenue(log.Address)
		if !ok || registration.Protocol != ProtocolVirtuals {
			return Event{}, false, nil
		}
		swap, err := decodeVirtualsBuy(log, registration)
		if err != nil {
			return Event{}, false, malformed("Virtuals buy", err)
		}
		return Event{Kind: EventVenueSwap, Protocol: ProtocolVirtuals, Data: swap, Log: log}, true, nil
	}

	if topic == topicPonsCurveBuy || topic == topicPonsCurveSell || topic == topicBagsCurveBuy || topic == topicBagsCurveSell {
		registration, ok := p.registry.LookupCurve(log.Address)
		if !ok {
			return Event{}, false, nil
		}
		buy := topic == topicPonsCurveBuy || topic == topicBagsCurveBuy
		var trade CurveTrade
		var err error
		switch {
		case registration.Protocol == ProtocolPonsV2 && (topic == topicPonsCurveBuy || topic == topicPonsCurveSell):
			trade, err = decodeCurveTrade(log)
		case registration.Protocol == ProtocolBagsV2 && (topic == topicBagsCurveBuy || topic == topicBagsCurveSell):
			trade, err = decodeBagsCurveTrade(log, buy)
		default:
			return Event{}, false, nil
		}
		if err != nil {
			return Event{}, false, malformed("curve trade", err)
		}
		kind := EventCurveSell
		if buy {
			kind = EventCurveBuy
		}
		return Event{Kind: kind, Protocol: registration.Protocol, Data: trade, Log: log}, true, nil
	}
	return Event{}, false, nil
}

func canonicalBagsPoolRegistration(initialized PoolInitialized, addresses AddressBook) (PoolRegistration, bool) {
	key := initialized.PoolKey
	if initialized.PoolID == (common.Hash{}) || key.Hooks != addresses.BagsHook || key.Fee != 1<<23 || key.TickSpacing != 60 {
		return PoolRegistration{}, false
	}
	var token common.Address
	switch {
	case key.Currency0 == addresses.WETH:
		token = key.Currency1
	case key.Currency1 == addresses.WETH:
		token = key.Currency0
	default:
		return PoolRegistration{}, false
	}
	if token == (common.Address{}) || token == addresses.WETH {
		return PoolRegistration{}, false
	}
	return PoolRegistration{
		PoolID: initialized.PoolID, Protocol: ProtocolBagsV2,
		Token: token, Quote: addresses.WETH, PoolKey: key,
	}, true
}

// canonicalPonsUSDGPoolRegistration recognizes graduated Pons meme pools that
// pair a meme token with USDG under the canonical Pons meme hook. This lets
// live Initialize logs register the pool without a same-receipt Launch event,
// which is required for catch-knife dump detection on already-traded pools.
func canonicalPonsUSDGPoolRegistration(initialized PoolInitialized, addresses AddressBook) (PoolRegistration, bool) {
	key := initialized.PoolKey
	if initialized.PoolID == (common.Hash{}) || key.Hooks != addresses.PonsMemeHook || key.TickSpacing == 0 || addresses.USDG == (common.Address{}) {
		return PoolRegistration{}, false
	}
	var token common.Address
	switch {
	case key.Currency0 == addresses.USDG:
		token = key.Currency1
	case key.Currency1 == addresses.USDG:
		token = key.Currency0
	default:
		return PoolRegistration{}, false
	}
	if token == (common.Address{}) || token == addresses.USDG || token == addresses.WETH {
		return PoolRegistration{}, false
	}
	return PoolRegistration{
		PoolID: initialized.PoolID, Protocol: ProtocolPonsV2,
		Token: token, Quote: addresses.USDG, PoolKey: key,
	}, true
}

// IsCanonicalBagsV4Pool reports whether a PoolKey can only be initialized by
// a bonding curve registered in the canonical Bags hook.
func IsCanonicalBagsV4Pool(key PoolKey) bool {
	id, err := PoolID(key)
	if err != nil {
		return false
	}
	_, ok := canonicalBagsPoolRegistration(PoolInitialized{PoolID: id, PoolKey: key}, DefaultAddressBook())
	return ok
}

// IsCanonicalPonsUSDGPool reports whether a PoolKey is a graduated Pons USDG
// meme pool under the canonical Pons meme hook.
func IsCanonicalPonsUSDGPool(key PoolKey) bool {
	id, err := PoolID(key)
	if err != nil {
		return false
	}
	_, ok := canonicalPonsUSDGPoolRegistration(PoolInitialized{PoolID: id, PoolKey: key}, DefaultAddressBook())
	return ok
}

// ParseLogFiltered preserves registry updates while suppressing output that
// does not match filter.
func (p *Parser) ParseLogFiltered(log gethtypes.Log, filter EventFilter) (Event, bool, error) {
	if p == nil || p.registry == nil || log.Removed || len(log.Topics) == 0 {
		return Event{}, false, nil
	}
	topic := log.Topics[0]
	if log.Address == p.addresses.PoolManager && topic == topicPoolSwap {
		if len(log.Topics) < 2 {
			return p.ParseLog(log)
		}
		registration, ok := p.registry.LookupPool(log.Topics[1])
		if !ok || !filter.matchClassification(registration.Protocol, EventSwap) {
			return Event{}, false, nil
		}
	} else if topic == topicPonsCurveBuy || topic == topicPonsCurveSell || topic == topicBagsCurveBuy || topic == topicBagsCurveSell {
		registration, ok := p.registry.LookupCurve(log.Address)
		if !ok {
			return Event{}, false, nil
		}
		kind := EventCurveSell
		if topic == topicPonsCurveBuy || topic == topicBagsCurveBuy {
			kind = EventCurveBuy
		}
		if !filter.matchClassification(registration.Protocol, kind) {
			return Event{}, false, nil
		}
	}
	event, ok, err := p.ParseLog(log)
	if err != nil || !ok || !filter.Match(event) {
		return Event{}, false, err
	}
	return event, true, nil
}

func (p *Parser) launchEvent(log gethtypes.Log, protocol Protocol, launch Launch, err error) (Event, bool, error) {
	if err != nil {
		return Event{}, false, fmt.Errorf("decode %s launch: %w", protocol, err)
	}
	if launch.Protocol != protocol || launch.Token == (common.Address{}) {
		return Event{}, false, fmt.Errorf("decode %s launch: %w", protocol, ErrMalformedLog)
	}
	staged := p.registry.fork()
	// PAIR can create several V4 pools for one token with distinct quotes in a
	// single receipt. TokenRegistration has one quote slot; keep its first
	// attribution and use the PoolID-keyed pending/pool records for each venue.
	prior, known := staged.LookupToken(launch.Token)
	additionalPAIRPool := protocol == ProtocolPAIR && launch.PoolID != (common.Hash{}) && known &&
		prior.Protocol == ProtocolPAIR && prior.Quote != (common.Address{}) && launch.Quote != (common.Address{}) &&
		prior.Quote != launch.Quote && prior.Venue == (common.Address{}) && launch.Venue == (common.Address{})
	if !additionalPAIRPool {
		if err := staged.RegisterToken(TokenRegistration{Token: launch.Token, Protocol: launch.Protocol, Quote: launch.Quote, Venue: launch.Venue}); err != nil {
			return Event{}, false, err
		}
	}
	if launch.Curve != (common.Address{}) {
		if err := staged.RegisterCurve(CurveRegistration{Curve: launch.Curve, Protocol: launch.Protocol, Token: launch.Token, Quote: launch.Quote}); err != nil {
			return Event{}, false, err
		}
	}
	if launch.PoolID != (common.Hash{}) {
		if err := staged.RegisterPendingPool(PendingPoolRegistration{PoolID: launch.PoolID, Protocol: launch.Protocol, Token: launch.Token, Quote: launch.Quote}); err != nil {
			return Event{}, false, err
		}
	}
	if err := p.registry.commit(staged); err != nil {
		return Event{}, false, err
	}
	return Event{Kind: EventLaunch, Protocol: launch.Protocol, Data: launch, Log: log}, true, nil
}

func (p *Parser) ParseReceipt(receipt *gethtypes.Receipt) ([]Event, error) {
	return p.parseReceiptWithLogErrorHandler(receipt, nil)
}

// ParseReceiptWithLogErrorHandler preserves ParseReceipt's transactional
// registry update while allowing explicitly recoverable log errors to be
// isolated from the rest of the receipt.
func (p *Parser) ParseReceiptWithLogErrorHandler(receipt *gethtypes.Receipt, handler LogErrorHandler) ([]Event, error) {
	return p.parseReceiptWithLogErrorHandler(receipt, handler)
}

func (p *Parser) parseReceiptWithLogErrorHandler(receipt *gethtypes.Receipt, handler LogErrorHandler) ([]Event, error) {
	if p == nil || p.registry == nil {
		return nil, ErrInvalidRegistration
	}
	if receipt == nil {
		return nil, fmt.Errorf("nil receipt")
	}
	staged := p.registry.fork()
	stagedParser := &Parser{addresses: p.addresses, registry: staged}
	events, err := stagedParser.parseReceipt(receipt, handler)
	if err != nil {
		return nil, err
	}
	if err := p.registry.commit(staged); err != nil {
		return nil, err
	}
	return events, nil
}

func (p *Parser) upgradeProtocol(proxy common.Address) (Protocol, bool) {
	switch proxy {
	case p.addresses.FlapController:
		return ProtocolFlapTax, true
	case p.addresses.VaroLaunchpad:
		return ProtocolVaro, true
	case p.addresses.VirtualsLaunchpad, p.addresses.VirtualsFactory, p.addresses.VirtualsFRouter:
		return ProtocolVirtuals, true
	case p.addresses.GMGNRouter:
		return ProtocolUnknown, true
	default:
		return ProtocolUnknown, false
	}
}

// ParseReceiptFiltered filters output after the transactional registry update,
// so hiding launch events cannot break later pool and curve attribution.
func (p *Parser) ParseReceiptFiltered(receipt *gethtypes.Receipt, filter EventFilter) ([]Event, error) {
	events, err := p.ParseReceipt(receipt)
	if err != nil {
		return nil, err
	}
	write := 0
	for _, event := range events {
		if filter.Match(event) {
			events[write] = event
			write++
		}
	}
	clear(events[write:])
	return events[:write], nil
}

func (p *Parser) parseReceipt(receipt *gethtypes.Receipt, handler LogErrorHandler) ([]Event, error) {
	flapEvents, flapConsumed, err := p.collectFlapReceiptEvents(receipt)
	if err != nil {
		return nil, err
	}
	events := make([]Event, 0, len(receipt.Logs)+len(flapEvents))
	events = append(events, flapEvents...)
	launchIndexes := make([]int, 0, 4)
	initializations := make(map[common.Hash]PoolInitialized)
	initializationIndexes := make(map[common.Hash]int)

	for receiptIndex, logPtr := range receipt.Logs {
		if logPtr == nil || logPtr.Removed {
			continue
		}
		if _, ok := flapConsumed[receiptIndex]; ok {
			continue
		}
		if len(logPtr.Topics) > 0 && logPtr.Address == p.addresses.PoolManager && logPtr.Topics[0] == topicPoolSwap {
			continue
		}
		event, ok, err := p.ParseLog(*logPtr)
		if err != nil {
			wrapped := fmt.Errorf("log %d: %w", logPtr.Index, err)
			if handler != nil && handler(*logPtr, wrapped) {
				continue
			}
			return nil, wrapped
		}
		if !ok {
			continue
		}
		if event.Kind == EventLaunch {
			launchIndexes = append(launchIndexes, len(events))
		}
		if event.Kind == EventPoolInitialized {
			initialized := event.Data.(PoolInitialized)
			initializations[initialized.PoolID] = initialized
			initializationIndexes[initialized.PoolID] = len(events)
		}
		events = append(events, event)
	}

	for _, index := range launchIndexes {
		launch := events[index].Data.(Launch)
		initialized, found := matchInitialization(launch, initializations)
		if !found {
			continue
		}
		launch.PoolID = initialized.PoolID
		poolKey := initialized.PoolKey
		launch.Pool = &poolKey
		if launch.Quote == (common.Address{}) {
			if launch.Token == poolKey.Currency0 {
				launch.Quote = poolKey.Currency1
			} else {
				launch.Quote = poolKey.Currency0
			}
		}
		registration := PoolRegistration{PoolID: launch.PoolID, Protocol: launch.Protocol, Token: launch.Token, Quote: launch.Quote, PoolKey: poolKey}
		if err := p.registry.RegisterPool(registration); err != nil {
			return nil, err
		}
		events[index].Data = launch
		if eventIndex, ok := initializationIndexes[launch.PoolID]; ok {
			events[eventIndex].Protocol = launch.Protocol
		}
	}

	for poolID, initialized := range initializations {
		if _, exists := p.registry.LookupPool(poolID); exists {
			continue
		}
		pending, found := p.registry.LookupPendingPool(poolID)
		if !found {
			continue
		}
		registration, err := poolRegistrationFromPending(pending, initialized)
		if err != nil {
			return nil, err
		}
		if err := p.registry.RegisterPool(registration); err != nil {
			return nil, err
		}
		if eventIndex, ok := initializationIndexes[poolID]; ok {
			events[eventIndex].Protocol = pending.Protocol
		}
	}

	for index := range events {
		if events[index].Kind != EventLiquidityModified {
			continue
		}
		modified := events[index].Data.(LiquidityModified)
		if registration, ok := p.registry.LookupPool(modified.PoolID); ok {
			events[index].Protocol = registration.Protocol
		}
	}

	for _, logPtr := range receipt.Logs {
		if logPtr == nil || logPtr.Removed || len(logPtr.Topics) == 0 || logPtr.Address != p.addresses.PoolManager || logPtr.Topics[0] != topicPoolSwap {
			continue
		}
		event, ok, err := p.ParseLog(*logPtr)
		if err != nil {
			wrapped := fmt.Errorf("log %d: %w", logPtr.Index, err)
			if handler != nil && handler(*logPtr, wrapped) {
				continue
			}
			return nil, wrapped
		}
		if ok {
			events = append(events, event)
		}
	}

	sort.SliceStable(events, func(i, j int) bool { return events[i].Log.Index < events[j].Log.Index })
	return events, nil
}

func poolRegistrationFromPending(pending PendingPoolRegistration, initialized PoolInitialized) (PoolRegistration, error) {
	if pending.Token != initialized.PoolKey.Currency0 && pending.Token != initialized.PoolKey.Currency1 {
		return PoolRegistration{}, ErrInvalidRegistration
	}
	quote := pending.Quote
	if quote == (common.Address{}) {
		if pending.Token == initialized.PoolKey.Currency0 {
			quote = initialized.PoolKey.Currency1
		} else {
			quote = initialized.PoolKey.Currency0
		}
	}
	return PoolRegistration{PoolID: initialized.PoolID, Protocol: pending.Protocol, Token: pending.Token, Quote: quote, PoolKey: initialized.PoolKey}, nil
}

func matchInitialization(launch Launch, candidates map[common.Hash]PoolInitialized) (PoolInitialized, bool) {
	if launch.PoolID != (common.Hash{}) {
		initialized, ok := candidates[launch.PoolID]
		return initialized, ok
	}
	var match PoolInitialized
	found := false
	for _, initialized := range candidates {
		key := initialized.PoolKey
		if launch.Token != key.Currency0 && launch.Token != key.Currency1 {
			continue
		}
		if launch.Quote != (common.Address{}) && launch.Quote != key.Currency0 && launch.Quote != key.Currency1 {
			continue
		}
		if found {
			return PoolInitialized{}, false
		}
		match, found = initialized, true
	}
	return match, found
}
