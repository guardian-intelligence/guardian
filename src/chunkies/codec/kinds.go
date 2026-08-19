package codec

// Framework event kinds: the system events the authority host mints
// itself, so their numbers are part of the framework contract every game
// module must speak, not game vocabulary. The kind space is partitioned —
// 0x0000–0x00FF framework, 0x0100 and up game — and these values are
// grandfathered from before the partition existed: journal history is
// written in them, so they keep their numbers inside the framework range.
const (
	// KindDayReset carries the UTC day index {day u32}. Wall clock enters
	// the sim only as this journaled event, never as a module read.
	KindDayReset uint16 = 5
	// KindEpochAdvance commits a module swap {epoch u32, module_hash u64}.
	KindEpochAdvance uint16 = 6
	// KindContentSet swaps the active content artifact {schema u32, id u64}.
	KindContentSet uint16 = 7
	// KindClockSkip repays authority downtime {to_tick u64}, forward only.
	KindClockSkip uint16 = 9
	// KindRateSet converges the world to a tick rate {hz u32}.
	KindRateSet uint16 = 10
)

// Doorman-level reject reasons, minted by the framework (gateway and
// authority) above the sim's own code space.
const (
	RejectReadOnly uint32 = 100
	RejectNotYours uint32 = 101
)
