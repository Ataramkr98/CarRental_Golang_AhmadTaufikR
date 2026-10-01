package handlers

import (
	"github.com/gofiber/fiber/v3"

	"car-rental-backend/middleware"
	"car-rental-backend/services"
)

type loginInput struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// Login authenticates an admin and returns a signed JWT.
func Login(c fiber.Ctx) error {
	var in loginInput
	if err := c.Bind().Body(&in); err != nil {
		return respondInvalid(c, "invalid request body")
	}

	session, err := services.Authenticate(in.Email, in.Password)
	if err != nil {
		return respondError(c, err)
	}
	return respondSuccess(c, fiber.StatusOK, session, "Signed in successfully")
}

type registerInput struct {
	Name     string `json:"name"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

// Register creates a new console account (when registration is enabled) and
// signs the caller in with the freshly created credentials.
func Register(c fiber.Ctx) error {
	var in registerInput
	if err := c.Bind().Body(&in); err != nil {
		return respondInvalid(c, "invalid request body")
	}

	session, err := services.Register(in.Name, in.Email, in.Password)
	if err != nil {
		return respondError(c, err)
	}
	return respondSuccess(c, fiber.StatusCreated, session, "Account created successfully")
}

type forgotPasswordInput struct {
	Email string `json:"email"`
}

// ForgotPassword starts the password-reset flow for an account.
func ForgotPassword(c fiber.Ctx) error {
	var in forgotPasswordInput
	if err := c.Bind().Body(&in); err != nil {
		return respondInvalid(c, "invalid request body")
	}

	result, err := services.RequestPasswordReset(in.Email)
	if err != nil {
		return respondError(c, err)
	}
	return respondSuccess(c, fiber.StatusOK, result, "If an account exists for that address, a reset link has been issued")
}

type resetPasswordInput struct {
	Token    string `json:"token"`
	Password string `json:"password"`
}

// ResetPassword consumes a reset token and stores the new password.
func ResetPassword(c fiber.Ctx) error {
	var in resetPasswordInput
	if err := c.Bind().Body(&in); err != nil {
		return respondInvalid(c, "invalid request body")
	}

	if err := services.ResetPassword(in.Token, in.Password); err != nil {
		return respondError(c, err)
	}
	return respondSuccess(c, fiber.StatusOK, fiber.Map{"reset": true}, "Your password has been changed — sign in with it now")
}

// Me returns the profile of the currently signed-in admin.
func Me(c fiber.Ctx) error {
	user, err := services.CurrentUser(middleware.UserID(c))
	if err != nil {
		return respondError(c, err)
	}
	return respondSuccess(c, fiber.StatusOK, user, "Profile retrieved")
}

type profileInput struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// UpdateProfile changes the signed-in admin's name and email address.
func UpdateProfile(c fiber.Ctx) error {
	var in profileInput
	if err := c.Bind().Body(&in); err != nil {
		return respondInvalid(c, "invalid request body")
	}

	user, err := services.UpdateProfile(middleware.UserID(c), in.Name, in.Email)
	if err != nil {
		return respondError(c, err)
	}
	return respondSuccess(c, fiber.StatusOK, user, "Profile updated successfully")
}

type passwordInput struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// ChangePassword rotates the signed-in admin's password.
func ChangePassword(c fiber.Ctx) error {
	var in passwordInput
	if err := c.Bind().Body(&in); err != nil {
		return respondInvalid(c, "invalid request body")
	}

	err := services.ChangePassword(middleware.UserID(c), in.CurrentPassword, in.NewPassword)
	if err != nil {
		return respondError(c, err)
	}
	return respondSuccess(c, fiber.StatusOK, fiber.Map{"updated": true}, "Password changed successfully")
}
