package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/cors"
	"github.com/gofiber/fiber/v3/middleware/logger"
	"github.com/gofiber/fiber/v3/middleware/recover"
	"github.com/joho/godotenv"

	"car-rental-backend/config"
	"car-rental-backend/handlers"
	"car-rental-backend/middleware"
)

func init() {
	// Load .env when present. In production the platform injects real
	// environment variables and this call is a harmless no-op.
	_ = godotenv.Load()
}

// allowedOrigins parses CORS_ORIGINS into a list.
// Defaults to "*" so a fresh local checkout works without configuration.
func allowedOrigins() []string {
	raw := strings.TrimSpace(os.Getenv("CORS_ORIGINS"))
	if raw == "" {
		return []string{"*"}
	}

	origins := make([]string, 0, 4)
	for _, part := range strings.Split(raw, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			origins = append(origins, trimmed)
		}
	}
	if len(origins) == 0 {
		return []string{"*"}
	}
	return origins
}

// setupApp connects the database, wires middleware and registers every route.
// It is shared by main() and the in-process API tests.
func setupApp() *fiber.App {
	config.Connect()

	app := fiber.New(fiber.Config{
		AppName:      "DriveNow Car Rental API",
		BodyLimit:    1 << 20, // 1 MiB is ample for this API's JSON payloads
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	})

	app.Use(logger.New())
	app.Use(recover.New())
	app.Use(middleware.SecurityHeaders())
	app.Use(cors.New(cors.Config{
		AllowOrigins: allowedOrigins(),
		AllowHeaders: []string{"Origin", "Content-Type", "Accept", "Authorization"},
		AllowMethods: []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
	}))

	// ---- health ----
	app.Get("/api/health", handlers.Health)

	// ---- runtime config (currency, payment mode) ----
	app.Get("/api/config", handlers.PublicConfig)

	// ---- public storefront ----
	app.Get("/api/cars", handlers.PublicCars)
	app.Post("/api/bookings", middleware.RateLimit("booking", 20, time.Minute), handlers.CreateBooking)
	app.Post("/api/auth/login", middleware.RateLimit("login", 10, time.Minute), handlers.Login)
	app.Post("/api/auth/register", middleware.RateLimit("register", 10, time.Minute), handlers.Register)
	app.Post("/api/auth/forgot-password", middleware.RateLimit("forgot", 10, time.Minute), handlers.ForgotPassword)
	app.Post("/api/auth/reset-password", middleware.RateLimit("reset", 10, time.Minute), handlers.ResetPassword)

	// ---- checkout + payment gateway callbacks ----
	// These routes serve two callers: a guest holding the booking's one-time
	// checkout token, and a signed-in operator using the console's "Mark paid"
	// action. OptionalAuth accepts whichever credential is present and the
	// handler decides, so no route here is reachable anonymously.
	app.Post("/api/rentals/:id/pay", middleware.OptionalAuth(), middleware.RateLimit("checkout", 30, time.Minute), handlers.CreateInvoice)
	app.Post("/api/rentals/:id/mock-pay", middleware.OptionalAuth(), middleware.RateLimit("checkout", 30, time.Minute), handlers.MockPay)
	app.Get("/api/rentals/:id/status", middleware.OptionalAuth(), handlers.RentalStatus)
	app.Post("/api/webhooks/xendit", handlers.XenditWebhook)

	// ---- admin console (JWT protected) ----
	admin := app.Group("/api/admin", middleware.JWTAuth())

	admin.Get("/me", handlers.Me)
	admin.Put("/profile", handlers.UpdateProfile)
	admin.Put("/password", handlers.ChangePassword)

	admin.Get("/dashboard/stats", handlers.DashboardStats)
	admin.Get("/analytics/revenue", handlers.RevenueSeries)
	admin.Get("/tracking", handlers.Tracking)
	admin.Get("/activity", handlers.ListActivity)

	admin.Get("/cars", handlers.ListCars)
	admin.Post("/cars", handlers.CreateCar)
	admin.Get("/cars/:id", handlers.GetCar)
	admin.Put("/cars/:id", handlers.UpdateCar)
	admin.Delete("/cars/:id", middleware.RequireAdmin(), handlers.DeleteCar)

	admin.Get("/customers", handlers.ListCustomers)
	admin.Post("/customers", handlers.CreateCustomer)
	admin.Get("/customers/:id", handlers.GetCustomer)
	admin.Put("/customers/:id", handlers.UpdateCustomer)
	admin.Delete("/customers/:id", middleware.RequireAdmin(), handlers.DeleteCustomer)

	admin.Get("/rentals", handlers.ListRentals)
	admin.Get("/rentals/export", handlers.ExportRentalsCSV)
	admin.Get("/rentals/:id", handlers.GetRental)
	admin.Put("/rentals/:id/borrow", handlers.BorrowRental)
	admin.Put("/rentals/:id/return", handlers.ReturnRental)
	admin.Put("/rentals/:id/cancel", handlers.CancelRental)

	admin.Get("/cashflow", handlers.ListCashFlow)
	admin.Get("/cashflow/summary", handlers.CashFlowSummary)
	admin.Get("/cashflow/export", handlers.ExportCashFlowCSV)
	admin.Post("/cashflow", handlers.CreateCashFlow)
	admin.Delete("/cashflow/:id", middleware.RequireAdmin(), handlers.DeleteCashFlow)

	// Unknown routes return the standard failure envelope rather than HTML.
	app.Use(func(c fiber.Ctx) error {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"success": false,
			"data":    nil,
			"message": "endpoint not found",
		})
	})

	return app
}

// runMigrations connects, migrates and seeds. Exported so the `-seed` flag and
// the test suite share one code path.
func runMigrations() {
	config.Connect()
}

// main supports a one-shot `-seed` mode: migrate, seed, exit. That is what a
// deployment job or a `make seed` target wants — starting a long-lived HTTP
// server just to create demo rows is a trap.
func main() {
	seedOnly := flag.Bool("seed", false, "migrate and seed the database, then exit")
	flag.Parse()

	runMigrations()
	if *seedOnly {
		config.Close()
		log.Println("Database migrated and seeded. Exiting (-seed).")
		return
	}

	app := setupApp()

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Println("DriveNow API listening on :" + port)
	if err := app.Listen(":"+port, fiber.ListenConfig{
		GracefulContext: ctx,
		ShutdownTimeout: 10 * time.Second,
	}); err != nil {
		log.Printf("Server stopped: %v", err)
	}

	config.Close()
	log.Println("Server shutdown complete.")
}
