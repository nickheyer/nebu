package services

import (
	"errors"
	"strconv"
	"strings"

	"connectrpc.com/connect"
)

// Keeps the items whose text holds every word of the query, case insensitive, every item for a blank query
func match[T any](items []T, query string, text func(T) []string) []T {
	words := strings.Fields(strings.ToLower(query))
	if len(words) == 0 {
		return items
	}
	out := make([]T, 0, len(items))
	for _, it := range items {
		hay := strings.ToLower(strings.Join(text(it), "\n"))
		kept := true
		for _, w := range words {
			if !strings.Contains(hay, w) {
				kept = false
				break
			}
		}
		if kept {
			out = append(out, it)
		}
	}
	return out
}

// Keeps the items the predicate admits
func keep[T any](items []T, admit func(T) bool) []T {
	out := make([]T, 0, len(items))
	for _, it := range items {
		if admit(it) {
			out = append(out, it)
		}
	}
	return out
}

// Cuts the page a cursor and limit name, limit zero meaning the rest, with the cursor of the page after
func page[T any](items []T, cursor string, limit uint32) ([]T, string, error) {
	start, err := offset(cursor)
	if err != nil {
		return nil, "", err
	}
	start = min(start, len(items))
	end := len(items)
	if limit > 0 && start+int(limit) < end {
		end = start + int(limit)
	}
	next := ""
	if end < len(items) {
		next = strconv.Itoa(end)
	}
	return items[start:end], next, nil
}

// The offset a cursor names, zero for an empty one, refused when it is not a count this list answered with
func offset(cursor string) (int, error) {
	if cursor == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(cursor)
	if err != nil || n < 0 {
		return 0, connect.NewError(connect.CodeInvalidArgument, errors.New("the cursor is not one this list answered with"))
	}
	return n, nil
}
