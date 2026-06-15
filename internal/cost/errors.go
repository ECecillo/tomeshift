package cost

import "errors"

// ErrBudgetExceeded is returned when a run's projected cost exceeds the
// configured per-book budget cap.
var ErrBudgetExceeded = errors.New("cost: projected spend exceeds per-book budget cap")
