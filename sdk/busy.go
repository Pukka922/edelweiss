package sdk

import (
	"sync"
	"sync/atomic"

	"github.com/gofiber/fiber/v3"
)

var busyCount atomic.Int64

// Busy marks the extension as busy until done is called. edelweissd does not
// stop a busy extension process for inactivity (e.g. during long jobs).
func Busy() (done func()) {
	busyCount.Add(1)
	var once sync.Once
	return func() { once.Do(func() { busyCount.Add(-1) }) }
}

func statusHandler(c fiber.Ctx) error {
	return c.JSON(fiber.Map{"busy": busyCount.Load() > 0})
}
