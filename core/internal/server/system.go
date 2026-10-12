package server

import (
	"time"

	"github.com/gofiber/fiber/v3"

	"github.com/Pukka922/edelweiss/sdk"
)

// Power restarts ("reboot") or shuts down ("poweroff") the host.
type Power func(action string) error

// power answers 202 and runs the action after Deps.PowerDelay, so that the answer reaches the browser first. A failure
// can only be logged then: the answer is gone.
func (s *Server) power(action string) fiber.Handler {
	return func(c fiber.Ctx) error {
		run := s.d.Power
		if run == nil {
			return sdk.NewError(fiber.StatusServiceUnavailable, "power_unavailable", "restarting and shutting down the host is not available")
		}
		user := sessionFrom(c).User.Name
		s.d.Logger.Info("host "+action+" requested", "user", user, "ip", c.IP())
		delay := s.d.PowerDelay
		go func() {
			time.Sleep(delay)
			if err := run(action); err != nil {
				s.d.Logger.Error("host "+action+" failed", "user", user, "err", err)
			}
		}()
		return c.Status(fiber.StatusAccepted).JSON(fiber.Map{"action": action})
	}
}
