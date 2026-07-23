package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"text/template"
	"time"
)

// ==========================================
// DYNAMIC MOCK GENERATION ENGINE
// ==========================================

var (
	// mockState tracks the "last generated value" for fields to create a random walk effect
	mockState   = make(map[string]float64)
	mockStateMu sync.Mutex

	// Regex to find unquoted template tags (e.g., `: {{.id}}`) and wrap them in quotes during structural parsing
	unquotedTmplRegex = regexp.MustCompile(`:\s*(\{\{.*?\}\})`)

	globalRateMultiplier   float64 = 1.0
	globalRateMultiplierMu sync.RWMutex
)

// EngineContext provides contextual data for template substitution
type EngineContext struct {
	Vars map[string]string // Dynamically extracted variables
}

func GetGlobalRateMultiplier() float64 {
	globalRateMultiplierMu.RLock()
	defer globalRateMultiplierMu.RUnlock()
	return globalRateMultiplier
}

func SetGlobalRateMultiplier(m float64) {
	if m <= 0 {
		m = 1.0 // Prevent division by zero or negative time
	}
	globalRateMultiplierMu.Lock()
	globalRateMultiplier = m
	globalRateMultiplierMu.Unlock()
}

// GenerateMockPayload takes a raw JSON template, applies context substitutions using text/template,
// applies randomizer rules (with stateful random walk tracking), and returns the final JSON bytes[cite: 4].
func GenerateMockPayload(templateKey string, rawTemplate json.RawMessage, rules map[string]RandomizerRule, ctx EngineContext) json.RawMessage {
	// 1. Execute Go template engine on the raw JSON string[cite: 4]
	tmpl, err := template.New("payload").Parse(string(rawTemplate))
	if err != nil {
		log.Printf("[Template] Parse error for %s: %v", templateKey, err)
		return rawTemplate
	}

	var tplBuf bytes.Buffer
	if err := tmpl.Execute(&tplBuf, ctx.Vars); err != nil {
		log.Printf("[Template] Execute error for %s: %v", templateKey, err)
		return rawTemplate
	}
	executedTemplate := tplBuf.Bytes()

	// 2. Unmarshal to map/array for randomizers[cite: 4]
	var data interface{}
	if err := json.Unmarshal(executedTemplate, &data); err != nil {
		// If it's not valid JSON (e.g., just a raw string), return the templated string directly[cite: 4]
		return json.RawMessage(executedTemplate)
	}

	// 3. Expand Arrays using "array_repeat" rule[cite: 4]
	expandArrays(data, rules)

	// 4. Apply randomizers[cite: 4]
	for fieldPath, rule := range rules {
		if rule.Type == "array_repeat" {
			continue // Already handled by expandArrays[cite: 4]
		}
		stateKey := fmt.Sprintf("%s:%s", templateKey, fieldPath)
		applyRandomizer(data, fieldPath, rule, stateKey)
	}

	// 5. Update dynamic timestamps if field exists (common convention)[cite: 4]
	updateCommonDynamicFields(data)

	// 6. Marshal back[cite: 4]
	res, err := json.Marshal(data)
	if err != nil {
		return json.RawMessage(executedTemplate)
	}
	return res
}

func applyRandomizer(node interface{}, path string, rule RandomizerRule, stateKey string) {
	parts := strings.Split(path, ".")
	walkAndMutate(node, parts, 0, rule, stateKey)
}

func walkAndMutate(node interface{}, path []string, index int, rule RandomizerRule, stateKey string) {
	if index == len(path) {
		return // shouldn't reach here directly[cite: 4]
	}

	key := path[index]
	isLast := index == len(path)-1

	switch v := node.(type) {
	case map[string]interface{}:
		val, ok := v[key]
		if !ok {
			return
		}

		if isLast {
			v[key] = generateRandomizedValue(val, rule, stateKey)
		} else {
			walkAndMutate(val, path, index+1, rule, stateKey)
		}

	case []interface{}:
		if key == "*" || key == "0" {
			for i, val := range v {
				itemStateKey := fmt.Sprintf("%s:%d", stateKey, i)
				if isLast {
					v[i] = generateRandomizedValue(val, rule, itemStateKey)
				} else {
					walkAndMutate(val, path, index+1, rule, itemStateKey)
				}
			}
			return
		}

		idx, err := strconv.Atoi(key)
		if err != nil || idx < 0 || idx >= len(v) {
			return
		}

		val := v[idx]
		if isLast {
			v[idx] = generateRandomizedValue(val, rule, stateKey)
		} else {
			walkAndMutate(val, path, index+1, rule, stateKey)
		}
	}
}

func expandArrays(node interface{}, rules map[string]RandomizerRule) {
	for fieldPath, rule := range rules {
		if rule.Type == "array_repeat" {
			parts := strings.Split(fieldPath, ".")
			walkAndExpandArray(node, parts, 0, rule)
		}
	}
}

func walkAndExpandArray(node interface{}, path []string, index int, rule RandomizerRule) {
	if index == len(path) {
		return
	}
	key := path[index]
	isLast := index == len(path)-1

	switch v := node.(type) {
	case map[string]interface{}:
		val, ok := v[key]
		if !ok {
			return
		}
		if isLast {
			if slice, ok := val.([]interface{}); ok && len(slice) == 1 {
				v[key] = duplicateSlice(slice, rule)
			}
		} else {
			walkAndExpandArray(val, path, index+1, rule)
		}
	case []interface{}:
		if key == "*" || key == "0" {
			for _, item := range v {
				if !isLast {
					walkAndExpandArray(item, path, index+1, rule)
				}
			}
		} else {
			idx, err := strconv.Atoi(key)
			if err == nil && idx >= 0 && idx < len(v) {
				if isLast {
					if slice, ok := v[idx].([]interface{}); ok && len(slice) == 1 {
						v[idx] = duplicateSlice(slice, rule)
					}
				} else {
					walkAndExpandArray(v[idx], path, index+1, rule)
				}
			}
		}
	}
}

func duplicateSlice(slice []interface{}, rule RandomizerRule) []interface{} {
	count := 1
	if rule.Min > 0 && rule.Max >= rule.Min {
		count = int(rule.Min) + rand.Intn(int(rule.Max-rule.Min+1))
	} else if rule.StaticVal != "" {
		if c, err := strconv.Atoi(rule.StaticVal); err == nil && c > 0 {
			count = c
		}
	} else if rule.Min > 0 {
		count = int(rule.Min)
	}

	if count <= 1 {
		return slice
	}

	templateItem := slice[0]
	newSlice := make([]interface{}, count)
	for i := 0; i < count; i++ {
		b, _ := json.Marshal(templateItem)
		var res interface{}
		json.Unmarshal(b, &res)
		newSlice[i] = res
	}
	return newSlice
}

func generateRandomizedValue(baseVal interface{}, rule RandomizerRule, stateKey string) interface{} {
	if rule.Type == "template_var" {
		return baseVal
	}

	if rule.Type == "static_string" {
		choices := strings.Split(rule.StaticVal, ",")
		if len(choices) > 0 {
			return strings.TrimSpace(choices[rand.Intn(len(choices))])
		}
		return rule.StaticVal
	}

	if rule.Type == "random_string" {
		return fmt.Sprintf("rand-%d", rand.Intn(999999))
	}

	if rule.Type == "timestamp" {
		return time.Now().UnixNano() / int64(time.Millisecond)
	}

	if rule.Type == "timestamp_string" {
		return fmt.Sprintf("%d", time.Now().UnixNano()/int64(time.Millisecond))
	}

	// Try to extract a numeric base value from the JSON[cite: 4]
	var baseNum float64
	switch v := baseVal.(type) {
	case float64:
		baseNum = v
	case int:
		baseNum = float64(v)
	case string:
		// Attempt to parse string as float. Replace comma with dot if necessary[cite: 4]
		cleaned := strings.Replace(v, ",", ".", 1)
		if parsed, err := strconv.ParseFloat(cleaned, 64); err == nil {
			baseNum = parsed
		} else {
			// If we can't parse it, default to a safe number or 100.0[cite: 4]
			baseNum = 100.0
		}
	default:
		baseNum = 100.0
	}

	mockStateMu.Lock()
	currentVal, exists := mockState[stateKey]
	if !exists {
		currentVal = baseNum
	}

	// Apply random walk variation[cite: 4]
	var newVal float64
	if rule.Percentage > 0 {
		variation := currentVal * (rule.Percentage / 100.0)
		offset := (rand.Float64() * 2 * variation) - variation
		newVal = currentVal + offset
	} else if rule.Max > rule.Min {
		newVal = rule.Min + rand.Float64()*(rule.Max-rule.Min)
	} else {
		newVal = currentVal // fallback[cite: 4]
	}

	// Prevent it from dropping below zero generally for prices/quantities[cite: 4]
	if newVal < 0 && rule.Min >= 0 {
		newVal = 0.001
	}

	mockState[stateKey] = newVal
	mockStateMu.Unlock()

	// Format output back based on rule type[cite: 4]
	switch rule.Type {
	case "int":
		return int64(newVal)
	case "double":
		return newVal
	case "double_string":
		// Guess the scale from the base string if it was a string[cite: 4]
		format := "%.2f"
		isComma := false
		if s, ok := baseVal.(string); ok {
			if strings.Contains(s, ",") {
				isComma = true
				parts := strings.Split(s, ",")
				format = fmt.Sprintf("%%.%df", len(parts[1]))
			} else if strings.Contains(s, ".") {
				parts := strings.Split(s, ".")
				format = fmt.Sprintf("%%.%df", len(parts[1]))
			} else {
				format = "%.0f"
			}
		}
		res := fmt.Sprintf(format, newVal)
		if isComma {
			res = strings.Replace(res, ".", ",", 1)
		}
		return res
	}

	return newVal
}

func updateCommonDynamicFields(node interface{}) {
	// Simple broad-stroke replacement for fields like timestamp[cite: 4]
	ts := time.Now().UnixNano() / int64(time.Millisecond)
	tsStr := fmt.Sprintf("%d", ts)

	var walk func(interface{})
	walk = func(n interface{}) {
		switch v := n.(type) {
		case map[string]interface{}:
			for k, val := range v {
				if k == "timestamp" || k == "updated_at" || k == "created_at" || k == "updated_time" {
					switch val.(type) {
					case string:
						v[k] = tsStr
					case float64:
						v[k] = float64(ts)
					}
				} else {
					walk(val)
				}
			}
		case []interface{}:
			for _, val := range v {
				walk(val)
			}
		}
	}
	walk(node)
}

// MatchPattern checks if a target string matches a wildcard pattern[cite: 4]
func MatchPattern(pattern, target string) (bool, []string) {
	if pattern == target {
		return true, nil
	}

	if !strings.Contains(pattern, "*") {
		return false, nil
	}

	parts := strings.Split(pattern, "*")
	if len(parts) == 0 {
		return false, nil
	}

	var args []string
	curr := target

	for i, part := range parts {
		if i == 0 {
			if !strings.HasPrefix(curr, part) {
				return false, nil
			}
			curr = curr[len(part):]
		} else if i == len(parts)-1 {
			if !strings.HasSuffix(curr, part) {
				return false, nil
			}
			if len(curr) > len(part) {
				args = append(args, curr[:len(curr)-len(part)])
			}
		} else {
			idx := strings.Index(curr, part)
			if idx == -1 {
				return false, nil
			}
			args = append(args, curr[:idx])
			curr = curr[idx+len(part):]
		}
	}

	return true, args
}

// ExtractVariablesFromRequest does a partial match between template and actual JSON request,
// returning a map of extracted string fields.
func ExtractVariablesFromRequest(template json.RawMessage, actual []byte) (bool, map[string]string) {
	var tplObj map[string]interface{}

	// 1. Try to unmarshal as a string first (handles escaped JSON strings from mocks.json)
	var tplStr string
	if err := json.Unmarshal(template, &tplStr); err == nil {
		// It is a stringified JSON. We temporarily wrap unquoted {{.var}} in quotes
		// ONLY in memory, JUST so Go can parse its structure to find the variables.
		processedTpl := unquotedTmplRegex.ReplaceAllString(tplStr, `: "$1"`)
		if err := json.Unmarshal([]byte(processedTpl), &tplObj); err != nil {
			log.Printf("[ExtractVars Debug] Failed to unmarshal processed string: %v", err)
			return false, nil
		}
	} else {
		// It is already a standard JSON object (like your watchlist config).
		if err := json.Unmarshal(template, &tplObj); err != nil {
			log.Printf("[ExtractVars Debug] Failed to unmarshal template object: %v", err)
			return false, nil
		}
	}

	var actObj map[string]interface{}
	if err := json.Unmarshal(actual, &actObj); err != nil {
		log.Printf("[ExtractVars Debug] Failed to unmarshal actual request: %v", err)
		return false, nil
	}

	vars := make(map[string]string)
	isMatch := true

	// Check if all fields in template exist in actual and match (or extract if variable)
	for k, v := range tplObj {
		actVal, ok := actObj[k]
		if !ok {
			isMatch = false
			break
		}

		if strVal, ok := v.(string); ok && strings.HasPrefix(strVal, "{{.") && strings.HasSuffix(strVal, "}}") {
			// Extract the variable value from the actual request
			varName := strings.TrimSuffix(strings.TrimPrefix(strVal, "{{."), "}}")
			vars[varName] = fmt.Sprintf("%v", actVal)
		} else {
			// Must match exactly
			if fmt.Sprintf("%v", v) != fmt.Sprintf("%v", actVal) {
				isMatch = false
				break
			}
		}
	}

	if !isMatch {
		actionMatched := false
		hasAction := false
		if tAction, ok := tplObj["action"]; ok {
			hasAction = true
			if aAction, ok := actObj["action"]; ok && tAction == aAction {
				actionMatched = true
			}
		}
		if tStreamAction, ok := tplObj["stream_action"]; ok {
			hasAction = true
			if aStreamAction, ok := actObj["stream_action"]; ok && tStreamAction == aStreamAction {
				actionMatched = true
			}
		}

		if hasAction && actionMatched {
			isMatch = true
			for k, v := range tplObj {
				if strVal, ok := v.(string); ok && strings.HasPrefix(strVal, "{{.") && strings.HasSuffix(strVal, "}}") {
					varName := strings.TrimSuffix(strings.TrimPrefix(strVal, "{{."), "}}")
					vars[varName] = fmt.Sprintf("%v", actObj[k])
				}
			}

			for k, v := range actObj {
				switch v.(type) {
				case string, float64, bool:
					vars[k] = fmt.Sprintf("%v", v)
				}
			}
		}
	} else {
		for k, v := range actObj {
			switch v.(type) {
			case string, float64, bool:
				vars[k] = fmt.Sprintf("%v", v)
			}
		}
	}

	// ==========================================
	// START CUSTOM WATCHLIST ARRAY PEEKING
	// ==========================================
	if items, ok := actObj["watchlist_items"].([]interface{}); ok && len(items) > 0 {
		if firstItem, ok := items[0].(map[string]interface{}); ok {
			if valStr, ok := firstItem["watchlist_item_value"].(string); ok {
				// Expecting format like "BTC/USDT"
				parts := strings.Split(valStr, "/")
				if len(parts) == 2 {
					vars["asset_id"] = parts[0]
					vars["quote_currency"] = parts[1]
				}
			}
		}
	}
	// ==========================================
	// END CUSTOM WATCHLIST ARRAY PEEKING
	// ==========================================

	return isMatch, vars
}

// ExtractVariablesFromChannel compares a channel string against a pattern
// containing variables like {{.asset_id}} and extracts them into a map.
func ExtractVariablesFromChannel(pattern, target string) (bool, map[string]string) {
	if pattern == target {
		return true, make(map[string]string)
	}

	// Convert {{.var_name}} into regex capture groups
	re := regexp.MustCompile(`\{\{\.([a-zA-Z0-9_]+)\}\}`)
	matches := re.FindAllStringSubmatch(pattern, -1)

	escapedPattern := regexp.QuoteMeta(pattern)
	for _, match := range matches {
		placeholder := regexp.QuoteMeta(match[0])
		varName := match[1]
		// Use lazy match .+? to stop at the next delimiter
		escapedPattern = strings.Replace(escapedPattern, placeholder, fmt.Sprintf(`(?P<%s>.+?)`, varName), 1)
	}

	compiledRe, err := regexp.Compile("^" + escapedPattern + "$")
	if err != nil {
		return false, nil
	}

	if !compiledRe.MatchString(target) {
		return false, nil
	}

	vars := make(map[string]string)
	submatches := compiledRe.FindStringSubmatch(target)
	names := compiledRe.SubexpNames()

	for i, name := range names {
		if i != 0 && name != "" {
			vars[name] = submatches[i]
		}
	}

	return true, vars
}
