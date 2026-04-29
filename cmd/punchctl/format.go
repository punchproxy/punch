package main

import (
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

type listFlags struct {
	output        string
	template      string
	fieldSelector string
	sortBy        string
}

func addListFlags(cmd *cobra.Command, flags *listFlags) {
	cmd.Flags().StringVarP(&flags.output, "output", "o", "", "output format: table, wide, json, yaml, jsonpath=..., custom-columns=..., go-template=...")
	cmd.Flags().StringVar(&flags.template, "template", "", "template string for -o go-template or -o go-template-file")
	cmd.Flags().StringVar(&flags.fieldSelector, "field-selector", "", "selector (field=value, field==value, field!=value) to filter rows")
	cmd.Flags().StringVar(&flags.sortBy, "sort-by", "", "sort rows by a field path, e.g. .status or {.status}; prefix with - for descending")
}

func prepareRows[T any](rows []T, fieldSelector, sortBy string) ([]T, error) {
	filtered, err := filterRows(rows, fieldSelector)
	if err != nil {
		return nil, err
	}
	if err := sortRows(filtered, sortBy); err != nil {
		return nil, err
	}
	return filtered, nil
}

type fieldRequirement struct {
	field string
	op    string
	value string
}

func filterRows[T any](rows []T, selector string) ([]T, error) {
	requirements, err := parseFieldSelector(selector)
	if err != nil {
		return nil, err
	}
	if len(requirements) == 0 {
		return rows, nil
	}
	filtered := make([]T, 0, len(rows))
	for _, row := range rows {
		matches := true
		for _, req := range requirements {
			value, ok := rowFieldString(row, req.field)
			if !ok {
				return nil, fmt.Errorf("unknown field selector %q", req.field)
			}
			switch req.op {
			case "=", "==":
				matches = value == req.value
			case "!=":
				matches = value != req.value
			}
			if !matches {
				break
			}
		}
		if matches {
			filtered = append(filtered, row)
		}
	}
	return filtered, nil
}

func parseFieldSelector(selector string) ([]fieldRequirement, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return nil, nil
	}
	parts := strings.Split(selector, ",")
	requirements := make([]fieldRequirement, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		op := ""
		for _, candidate := range []string{"!=", "==", "="} {
			if strings.Contains(part, candidate) {
				op = candidate
				break
			}
		}
		if op == "" {
			return nil, fmt.Errorf("invalid field selector %q: expected field=value, field==value, or field!=value", part)
		}
		sides := strings.SplitN(part, op, 2)
		field := normalizeFieldPath(sides[0])
		value := strings.TrimSpace(sides[1])
		if field == "" || value == "" {
			return nil, fmt.Errorf("invalid field selector %q: field and value are required", part)
		}
		requirements = append(requirements, fieldRequirement{field: field, op: op, value: value})
	}
	return requirements, nil
}

func sortRows[T any](rows []T, sortBy string) error {
	field, desc := parseSortBy(sortBy)
	if field == "" {
		return nil
	}
	if len(rows) > 0 {
		if _, ok := rowFieldValue(rows[0], field); !ok {
			return fmt.Errorf("unknown sort field %q", field)
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		left, _ := rowFieldValue(rows[i], field)
		right, _ := rowFieldValue(rows[j], field)
		leftMissing := isMissingSortValue(left)
		rightMissing := isMissingSortValue(right)
		if leftMissing || rightMissing {
			return !leftMissing && rightMissing
		}
		cmp := compareValues(left, right)
		if desc {
			return cmp > 0
		}
		return cmp < 0
	})
	return nil
}

func parseSortBy(sortBy string) (string, bool) {
	sortBy = strings.TrimSpace(sortBy)
	desc := false
	if strings.HasPrefix(sortBy, "-") {
		desc = true
		sortBy = strings.TrimSpace(strings.TrimPrefix(sortBy, "-"))
	}
	return normalizeFieldPath(sortBy), desc
}

func normalizeFieldPath(path string) string {
	path = strings.TrimSpace(path)
	path = strings.TrimPrefix(path, "{")
	path = strings.TrimSuffix(path, "}")
	path = strings.TrimPrefix(path, ".")
	return strings.TrimSpace(path)
}

func rowFieldString(row any, field string) (string, bool) {
	value, ok := rowFieldValue(row, field)
	if !ok {
		return "", false
	}
	return valueToString(value), true
}

func rowFieldValue(row any, field string) (reflect.Value, bool) {
	value := reflect.ValueOf(row)
	for value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return reflect.Value{}, false
		}
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return reflect.Value{}, false
	}
	typ := value.Type()
	fieldKey := normalizeFieldName(field)
	for i := 0; i < value.NumField(); i++ {
		sf := typ.Field(i)
		if sf.PkgPath != "" {
			continue
		}
		if fieldMatches(sf, fieldKey) {
			return value.Field(i), true
		}
	}
	return reflect.Value{}, false
}

func fieldMatches(field reflect.StructField, key string) bool {
	if normalizeFieldName(field.Name) == key {
		return true
	}
	for _, tagName := range []string{"json", "yaml"} {
		tag := field.Tag.Get(tagName)
		if tag == "" {
			continue
		}
		name := strings.Split(tag, ",")[0]
		if name != "" && name != "-" && normalizeFieldName(name) == key {
			return true
		}
	}
	return false
}

func normalizeFieldName(name string) string {
	name = strings.TrimSpace(strings.ToLower(name))
	replacer := strings.NewReplacer("_", "", "-", "", ".", "")
	return replacer.Replace(name)
}

func compareValues(left, right reflect.Value) int {
	for left.Kind() == reflect.Pointer {
		if left.IsNil() {
			return -1
		}
		left = left.Elem()
	}
	for right.Kind() == reflect.Pointer {
		if right.IsNil() {
			return 1
		}
		right = right.Elem()
	}
	if left.IsValid() && right.IsValid() && left.Type() == reflect.TypeOf(time.Time{}) {
		l := left.Interface().(time.Time)
		r := right.Interface().(time.Time)
		return l.Compare(r)
	}
	switch left.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return compareFloat(float64(left.Int()), float64(right.Int()))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return compareFloat(float64(left.Uint()), float64(right.Uint()))
	case reflect.Float32, reflect.Float64:
		return compareFloat(left.Float(), right.Float())
	case reflect.Bool:
		if left.Bool() == right.Bool() {
			return 0
		}
		if !left.Bool() {
			return -1
		}
		return 1
	default:
		return compareString(valueToString(left), valueToString(right))
	}
}

func compareFloat(left, right float64) int {
	if left < right {
		return -1
	}
	if left > right {
		return 1
	}
	return 0
}

func compareString(left, right string) int {
	leftNumber, leftOK := leadingNumber(left)
	rightNumber, rightOK := leadingNumber(right)
	if leftOK && rightOK && leftNumber != rightNumber {
		return compareFloat(leftNumber, rightNumber)
	}
	return strings.Compare(left, right)
}

// byteSize is an int64 byte count that renders as a human-readable size.
// Carrying the raw value (not a pre-formatted string) lets sort/compare
// operate numerically.
type byteSize int64

func (b byteSize) String() string { return formatBytes(int64(b)) }

// latencyMS is a millisecond latency. Non-positive values mean "no
// measurement" and sort as missing.
type latencyMS int64

func (l latencyMS) String() string  { return formatLatency(int64(l)) }
func (l latencyMS) IsMissing() bool { return int64(l) <= 0 }

// durationMS is a millisecond duration that renders as a coarse human
// duration.
type durationMS int64

func (d durationMS) String() string { return formatDurationMS(int64(d)) }

func isMissingSortValue(value reflect.Value) bool {
	if !value.IsValid() {
		return true
	}
	for value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return true
		}
		value = value.Elem()
	}
	if value.CanInterface() {
		if m, ok := value.Interface().(interface{ IsMissing() bool }); ok {
			return m.IsMissing()
		}
	}
	if value.Type() == reflect.TypeOf(time.Time{}) {
		return value.Interface().(time.Time).IsZero()
	}
	if value.Kind() != reflect.String {
		return false
	}
	switch strings.TrimSpace(value.String()) {
	case "", "-":
		return true
	default:
		return false
	}
}

func leadingNumber(value string) (float64, bool) {
	value = strings.TrimSpace(value)
	if value == "" || value == "-" || value == "expired" {
		return 0, false
	}
	end := 0
	for end < len(value) {
		ch := value[end]
		if (ch < '0' || ch > '9') && ch != '.' && ch != '-' {
			break
		}
		end++
	}
	if end == 0 {
		return 0, false
	}
	number, err := strconv.ParseFloat(value[:end], 64)
	return number, err == nil
}

func valueToString(value reflect.Value) string {
	if !value.IsValid() {
		return ""
	}
	for value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return ""
		}
		value = value.Elem()
	}
	if value.Type() == reflect.TypeOf(time.Time{}) {
		return formatTime(value.Interface().(time.Time))
	}
	return fmt.Sprint(value.Interface())
}

// splitDomainMatchers turns a comma-separated flag value into a list of
// trimmed, non-empty matchers (e.g. "google.com, full:x.com" -> two entries).
func splitDomainMatchers(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			result = append(result, part)
		}
	}
	return result
}

// formatLatency renders a millisecond count as e.g. "12ms", or "-" when
// the upstream has no recorded latency yet.
func formatLatency(ms int64) string {
	if ms <= 0 {
		return "-"
	}
	return fmt.Sprintf("%dms", ms)
}

// formatOptional returns "-" for empty strings, the value otherwise.
func formatOptional(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

// formatTimestamp parses an RFC3339Nano timestamp and renders it in the
// local timezone. Empty or unparseable input renders as "-".
func formatTimestamp(value string) string {
	if value == "" {
		return "-"
	}
	ts, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return "-"
	}
	return ts.Local().Format("2006-01-02 15:04:05")
}

// formatTime renders a time.Time in the local timezone. The zero value
// renders as "-".
func formatTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Local().Format("2006-01-02 15:04:05")
}

// formatRemaining renders the duration from now until t, rounded to
// seconds. Returns "expired" for past times and "-" for the zero value.
func formatRemaining(now, t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := t.Sub(now)
	if d <= 0 {
		return "expired"
	}
	return d.Round(time.Second).String()
}
