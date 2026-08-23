// Package progress renders a run's advancement on the terminal while it is
// happening.
//
// A book is minutes to hours of work and the CLI is otherwise silent for all of
// it: the structured log goes to logs.ndjson and stdout only carries the
// summary, once the run is over. The bar is a human affordance, never a data
// source — it writes to stderr so stdout stays parsable, and it draws nothing
// at all when the stream is not a terminal.
package progress
