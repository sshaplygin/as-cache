// Package bandit provides ready-made [ascache.Bandit] implementations.
//
// The core as-cache module deliberately ships none: which arm to pull is the
// interesting decision, and it depends on how fast the traffic moves. This
// module makes the common choices available without putting them, or their
// dependencies, in the core.
//
// # Local
//
// [Thompson] samples each arm's hit rate from a Beta posterior and picks the
// best draw, so an arm is chosen roughly as often as it is likely to be the
// best one. Evidence is discounted as it ages, which is what lets it change
// its mind when the workload does. [Greedy] always takes the best-measured arm
// and exists as a control: it shows what the adaptive layer achieves with no
// exploration at all.
//
// # What crosses the wire
//
// Nothing. These bandits are in-process: they see per-policy hit and miss
// counts and return a policy name.
package bandit
