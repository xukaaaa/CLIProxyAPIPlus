package cmd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	iflowauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/iflow"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func DoIFlowCookieAuth(cfg *config.Config, options *LoginOptions) {
	if options == nil {
		options = &LoginOptions{}
	}

	promptFn := options.Prompt
	if promptFn == nil {
		reader := bufio.NewReader(os.Stdin)
		promptFn = func(prompt string) (string, error) {
			fmt.Print(prompt)
			value, err := reader.ReadString('\n')
			if err != nil {
				return "", err
			}
			return strings.TrimSpace(value), nil
		}
	}

	cookie, err := promptForIFlowCookie(promptFn)
	if err != nil {
		fmt.Printf("Failed to get cookie: %v\n", err)
		return
	}

	bxAuth := iflowauth.ExtractBXAuth(cookie)
	if existingFile, err := iflowauth.CheckDuplicateBXAuth(cfg.AuthDir, bxAuth); err != nil {
		fmt.Printf("Failed to check duplicate: %v\n", err)
		return
	} else if existingFile != "" {
		fmt.Printf("Duplicate BXAuth found, authentication already exists: %s\n", filepath.Base(existingFile))
		return
	}

	auth := iflowauth.NewIFlowAuth(cfg)
	tokenData, err := auth.AuthenticateWithCookie(context.Background(), cookie)
	if err != nil {
		fmt.Printf("iFlow cookie authentication failed: %v\n", err)
		return
	}

	tokenStorage := auth.CreateCookieTokenStorage(tokenData)
	authFilePath := getIFlowAuthFilePath(cfg, "iflow", tokenData.Email)
	if err := tokenStorage.SaveTokenToFile(authFilePath); err != nil {
		fmt.Printf("Failed to save authentication: %v\n", err)
		return
	}

	fmt.Printf("Authentication successful! API key: %s\n", tokenData.APIKey)
	fmt.Printf("Expires at: %s\n", tokenData.Expire)
	fmt.Printf("Authentication saved to: %s\n", authFilePath)
}

func promptForIFlowCookie(promptFn func(string) (string, error)) (string, error) {
	line, err := promptFn("Enter iFlow Cookie (from browser cookies): ")
	if err != nil {
		return "", fmt.Errorf("failed to read cookie: %w", err)
	}
	return iflowauth.NormalizeCookie(line)
}

func getIFlowAuthFilePath(cfg *config.Config, provider, email string) string {
	fileName := iflowauth.SanitizeIFlowFileName(email)
	return fmt.Sprintf("%s/%s-%s-%d.json", cfg.AuthDir, provider, fileName, time.Now().Unix())
}
