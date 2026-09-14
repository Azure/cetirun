// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package azure

import (
	"context"
	"errors"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/botservice/armbotservice"
)

// BotServiceClient wraps the ARM Bot Service SDK for creating and managing
// bot registrations and Teams channels.
type BotServiceClient struct {
	bots     *armbotservice.BotsClient
	channels *armbotservice.ChannelsClient
}

// NewBotServiceClient creates a new Bot Service client.
func NewBotServiceClient(cred azcore.TokenCredential, subscriptionID string) (*BotServiceClient, error) {
	opts := &arm.ClientOptions{}
	bots, err := armbotservice.NewBotsClient(subscriptionID, cred, opts)
	if err != nil {
		return nil, fmt.Errorf("create bots client: %w", err)
	}
	channels, err := armbotservice.NewChannelsClient(subscriptionID, cred, opts)
	if err != nil {
		return nil, fmt.Errorf("create channels client: %w", err)
	}
	return &BotServiceClient{
		bots:     bots,
		channels: channels,
	}, nil
}

// EnsureBotService creates or updates an Azure Bot Service registration.
// endpoint is the messaging endpoint (e.g. https://alice.example.com/api/messages).
// msaAppID is the Azure AD application (client) ID for the bot.
// msaAppTenantID is the Azure AD tenant ID.
// Returns the ARM resource ID of the bot.
func (c *BotServiceClient) EnsureBotService(ctx context.Context, resourceGroup, botName, endpoint, msaAppID, msaAppTenantID string) (string, error) {
	bot := armbotservice.Bot{
		Location: toPtr("global"),
		Kind:     toPtr(armbotservice.KindAzurebot),
		SKU: &armbotservice.SKU{
			Name: toPtr(armbotservice.SKUNameF0),
		},
		Properties: &armbotservice.BotProperties{
			DisplayName:          &botName,
			Endpoint:             &endpoint,
			MsaAppID:             &msaAppID,
			MsaAppTenantID:       &msaAppTenantID,
			MsaAppType:           toPtr(armbotservice.MsaAppTypeSingleTenant),
			DisableLocalAuth:     toPtr(true),
			IsStreamingSupported: toPtr(false),
		},
	}

	resp, err := c.bots.Create(ctx, resourceGroup, botName, bot, nil)
	if err != nil {
		return "", fmt.Errorf("create/update bot %s: %w", botName, err)
	}
	if resp.ID == nil {
		return "", fmt.Errorf("bot %s created but response has no ID", botName)
	}
	return *resp.ID, nil
}

// EnsureTeamsChannel creates or updates the Microsoft Teams channel on a bot.
func (c *BotServiceClient) EnsureTeamsChannel(ctx context.Context, resourceGroup, botName string) error {
	channel := armbotservice.BotChannel{
		Location: toPtr("global"),
		Properties: &armbotservice.MsTeamsChannel{
			ChannelName: toPtr("MsTeamsChannel"),
			Properties: &armbotservice.MsTeamsChannelProperties{
				IsEnabled: toPtr(true),
			},
		},
	}

	_, err := c.channels.Create(ctx, resourceGroup, botName, armbotservice.ChannelNameMsTeamsChannel, channel, nil)
	if err != nil {
		return fmt.Errorf("create/update Teams channel on bot %s: %w", botName, err)
	}
	return nil
}

// DeleteBotService deletes a Bot Service registration. Returns nil if not found (404).
func (c *BotServiceClient) DeleteBotService(ctx context.Context, resourceGroup, botName string) error {
	_, err := c.bots.Delete(ctx, resourceGroup, botName, nil)
	if err != nil {
		if isARMNotFound(err) {
			return nil
		}
		return fmt.Errorf("delete bot %s: %w", botName, err)
	}
	return nil
}

// DeleteTeamsChannel deletes the Teams channel from a bot. Returns nil if not found (404).
func (c *BotServiceClient) DeleteTeamsChannel(ctx context.Context, resourceGroup, botName string) error {
	_, err := c.channels.Delete(ctx, resourceGroup, botName, string(armbotservice.ChannelNameMsTeamsChannel), nil)
	if err != nil {
		if isARMNotFound(err) {
			return nil
		}
		return fmt.Errorf("delete Teams channel on bot %s: %w", botName, err)
	}
	return nil
}

// isARMNotFound checks if an ARM SDK error is a 404.
func isARMNotFound(err error) bool {
	var respErr *azcore.ResponseError
	if errors.As(err, &respErr) {
		return respErr.StatusCode == 404
	}
	return false
}

func toPtr[T any](v T) *T {
	return &v
}
