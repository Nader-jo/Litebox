// Package mail contains provider-neutral email normalization and safety rules.
package mail

import (
	"fmt"
	stdmail "net/mail"
	"sort"
	"strings"

	"github.com/Nader-jo/Litebox/internal/model"
)

// ParseAddresses parses RFC-compatible comma-separated mailbox input and deduplicates it.
func ParseAddresses(input string) ([]model.Address, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, nil
	}
	parsed, err := stdmail.ParseAddressList(input)
	if err != nil {
		return nil, fmt.Errorf("parse recipients: %w", err)
	}
	seen := make(map[string]struct{}, len(parsed))
	result := make([]model.Address, 0, len(parsed))
	for _, address := range parsed {
		normalized := strings.ToLower(strings.TrimSpace(address.Address))
		if strings.ContainsAny(normalized, "\r\n") {
			return nil, fmt.Errorf("recipient contains a header injection character")
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		result = append(result, model.Address{Name: strings.TrimSpace(address.Name), Address: normalized})
	}
	return result, nil
}

// ParseOne parses one address while preserving a display name.
func ParseOne(input string) (model.Address, error) {
	address, err := stdmail.ParseAddress(strings.TrimSpace(input))
	if err != nil {
		return model.Address{}, err
	}
	return model.Address{Name: strings.TrimSpace(address.Name), Address: strings.ToLower(address.Address)}, nil
}

// FormatAddresses returns conventional human-readable mailbox syntax.
func FormatAddresses(addresses []model.Address) string {
	values := make([]string, 0, len(addresses))
	for _, address := range addresses {
		values = append(values, (&stdmail.Address{Name: address.Name, Address: address.Address}).String())
	}
	return strings.Join(values, ", ")
}

// IsAllowedRecipient checks all envelope-visible recipients against an allow-list.
func IsAllowedRecipient(addresses []model.Address, allowed map[string]struct{}) bool {
	for _, address := range addresses {
		if _, ok := allowed[strings.ToLower(strings.TrimSpace(address.Address))]; ok {
			return true
		}
	}
	return false
}

// ReplyAll builds a stable deduplicated recipient set while excluding the mailbox itself.
func ReplyAll(replyTo, from model.Address, originalTo, originalCC []model.Address, own map[string]struct{}) []model.Address {
	primary := from
	if replyTo.Address != "" {
		primary = replyTo
	}
	candidates := append([]model.Address{primary}, append(originalTo, originalCC...)...)
	unique := make(map[string]model.Address)
	for _, address := range candidates {
		key := strings.ToLower(strings.TrimSpace(address.Address))
		if key == "" {
			continue
		}
		if _, excluded := own[key]; excluded {
			continue
		}
		if _, exists := unique[key]; !exists {
			address.Address = key
			unique[key] = address
		}
	}
	keys := make([]string, 0, len(unique))
	for key := range unique {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]model.Address, 0, len(keys))
	if value, ok := unique[strings.ToLower(primary.Address)]; ok {
		result = append(result, value)
		delete(unique, strings.ToLower(primary.Address))
	}
	for _, key := range keys {
		if value, ok := unique[key]; ok {
			result = append(result, value)
		}
	}
	return result
}
