package handlers

import (
	"github.com/gofiber/fiber/v3"

	"car-rental-backend/services"
)

// ListCustomers returns customers with optional ?search=, ?sort= and
// ?page=/?per_page= pagination.
func ListCustomers(c fiber.Ctx) error {
	page, paged, err := services.ParsePage(c, 20, 100)
	if err != nil {
		return respondError(c, err)
	}

	customers, err := services.ListCustomers(services.CustomerFilter{
		Search: c.Query("search"),
		Sort:   c.Query("sort"),
		Page:   page,
		Paged:  paged,
	})
	if err != nil {
		return respondError(c, err)
	}
	return respondSuccess(c, fiber.StatusOK, customers, "Customers retrieved successfully")
}

// GetCustomer returns one customer together with their rental history.
func GetCustomer(c fiber.Ctx) error {
	id, err := pathID(c)
	if err != nil {
		return respondError(c, err)
	}
	detail, err := services.GetCustomerDetail(id)
	if err != nil {
		return respondError(c, err)
	}
	return respondSuccess(c, fiber.StatusOK, detail, "Customer retrieved successfully")
}

// CreateCustomer adds a customer record.
func CreateCustomer(c fiber.Ctx) error {
	var in services.CustomerInput
	if err := c.Bind().Body(&in); err != nil {
		return respondInvalid(c, "invalid request body")
	}

	customer, err := services.CreateCustomer(in)
	if err != nil {
		return respondError(c, err)
	}
	return respondSuccess(c, fiber.StatusCreated, customer, "Customer added successfully")
}

// UpdateCustomer applies a partial update to a customer record.
func UpdateCustomer(c fiber.Ctx) error {
	id, err := pathID(c)
	if err != nil {
		return respondError(c, err)
	}
	var patch services.CustomerPatch
	if err := c.Bind().Body(&patch); err != nil {
		return respondInvalid(c, "invalid request body")
	}

	customer, err := services.UpdateCustomer(id, patch)
	if err != nil {
		return respondError(c, err)
	}
	return respondSuccess(c, fiber.StatusOK, customer, "Customer updated successfully")
}

// DeleteCustomer removes a customer with no rental history.
func DeleteCustomer(c fiber.Ctx) error {
	id, err := pathID(c)
	if err != nil {
		return respondError(c, err)
	}
	if err := services.DeleteCustomer(id); err != nil {
		return respondError(c, err)
	}
	return respondSuccess(c, fiber.StatusOK, fiber.Map{"deleted": true}, "Customer deleted successfully")
}
