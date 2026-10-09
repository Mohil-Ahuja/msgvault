package carddav

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/vcard"
)

const (
	maxPublicCardDAVValueBytes = 256
	maxPublicContactValues     = 8
)

type ConflictSideState string

const (
	ConflictSidePresent     ConflictSideState = "present"
	ConflictSideDeleted     ConflictSideState = "deleted"
	ConflictSideUnavailable ConflictSideState = "unavailable"
)

type ContactField struct {
	Name   string   `json:"name"`
	Value  string   `json:"value"`
	Labels []string `json:"labels"`
}

type ContactSummary struct {
	State       ConflictSideState `json:"state"`
	DisplayName string            `json:"display_name,omitempty"`
	Emails      []string          `json:"emails"`
	Phones      []string          `json:"phones"`
	Fields      []ContactField    `json:"fields,omitempty"`
	Truncated   bool              `json:"truncated,omitzero"`
}

type AddressBookIdentity struct {
	ID   int64
	Name string
}

type ConflictListItem struct {
	ID                 int64
	AddressBook        AddressBookIdentity
	Status             store.CardDAVConflictStatus
	LocalState         ConflictSideState
	RemoteState        ConflictSideState
	AllowedResolutions []ResolutionChoice
	UpdatedAt          time.Time
}

type ConflictDetail struct {
	ID                 int64
	AddressBook        AddressBookIdentity
	Status             store.CardDAVConflictStatus
	Resolution         store.CardDAVConflictResolution
	Base               ContactSummary
	Local              ContactSummary
	Remote             ContactSummary
	AllowedResolutions []ResolutionChoice
	CreatedAt          time.Time
	UpdatedAt          time.Time
	ResolvedAt         *time.Time
}

func publicAddressBookIdentity(id int64, name string) AddressBookIdentity {
	name = normalizePublicCardDAVText(name)
	name, _ = capPublicCardDAVText(name)
	return AddressBookIdentity{ID: id, Name: name}
}

func emptyContactSummary(state ConflictSideState) ContactSummary {
	return ContactSummary{State: state, Emails: []string{}, Phones: []string{}}
}

func projectConflictContact(body []byte, tombstone bool) ContactSummary {
	if tombstone {
		return emptyContactSummary(ConflictSideDeleted)
	}
	envelope, err := vcard.ParseResourceEnvelope(body)
	if err != nil {
		return emptyContactSummary(ConflictSideUnavailable)
	}
	return projectConflictEnvelope(envelope, nil)
}

func projectConflictEnvelope(envelope vcard.ResourceEnvelope, changed map[string]bool) ContactSummary {
	summary := emptyContactSummary(ConflictSidePresent)
	emails := make(map[string]struct{})
	phones := make(map[string]struct{})
	occurrences := append([]vcard.PropertyOccurrence(nil), envelope.PropertyTree...)
	slices.SortStableFunc(occurrences, func(a, b vcard.PropertyOccurrence) int {
		if changed[strings.ToUpper(a.Property.Name)] == changed[strings.ToUpper(b.Property.Name)] {
			return 0
		}
		if changed[strings.ToUpper(a.Property.Name)] {
			return -1
		}
		return 1
	})
	for _, occurrence := range occurrences {
		property := occurrence.Property
		name := strings.ToUpper(property.Name)
		switch name {
		case "FN", "EMAIL", "TEL", "ADR", "N", "ORG", "TITLE", "ROLE", "BDAY", "ANNIVERSARY", "CATEGORIES":
		default:
			continue
		}
		value, err := publicConflictPropertyValue(envelope.RenderMetadata.StoredVersion, property)
		if err != nil {
			return emptyContactSummary(ConflictSideUnavailable)
		}
		switch name {
		case "EMAIL":
			value = vcard.TrimPrefixFold(strings.TrimSpace(value), "mailto:")
		case "TEL":
			value = vcard.TrimPrefixFold(strings.TrimSpace(value), "tel:")
		}
		value = normalizePublicCardDAVText(value)
		if value == "" {
			continue
		}
		labels := []string{}
		for _, parameter := range property.Parameters {
			paramName := strings.ToUpper(parameter.Name)
			switch paramName {
			case "TYPE", "PREF", "LABEL", "LANGUAGE", "SCRIPT", "PHONETIC", "SORT-AS", "GEO", "TZ", "CC":
			default:
				continue
			}
			for _, paramValue := range parameter.Values {
				if len(labels) == maxPublicContactValues {
					summary.Truncated = true
					break
				}
				label, clipped := capPublicCardDAVText(normalizePublicCardDAVText(paramName + "=" + paramValue.Decoded))
				summary.Truncated = summary.Truncated || clipped
				labels = append(labels, label)
			}
		}
		if changed[name] || len(labels) > 0 || (name != "FN" && name != "EMAIL" && name != "TEL") {
			if len(summary.Fields) < maxPublicContactValues {
				fieldValue, clipped := capPublicCardDAVText(value)
				summary.Truncated = summary.Truncated || clipped
				summary.Fields = append(summary.Fields, ContactField{Name: name, Value: fieldValue, Labels: labels})
			} else {
				summary.Truncated = true
			}
		}
		if name == "FN" && summary.DisplayName != "" {
			continue
		}
		dedupeKey := value
		clipped, truncated := capPublicCardDAVText(value)
		summary.Truncated = summary.Truncated || truncated
		switch name {
		case "FN":
			if summary.DisplayName == "" {
				summary.DisplayName = clipped
			}
		case "EMAIL":
			if _, exists := emails[dedupeKey]; exists {
				continue
			}
			if len(summary.Emails) == maxPublicContactValues {
				summary.Truncated = true
				continue
			}
			emails[dedupeKey] = struct{}{}
			summary.Emails = append(summary.Emails, clipped)
		case "TEL":
			if _, exists := phones[dedupeKey]; exists {
				continue
			}
			if len(summary.Phones) == maxPublicContactValues {
				summary.Truncated = true
				continue
			}
			phones[dedupeKey] = struct{}{}
			summary.Phones = append(summary.Phones, clipped)
		}
	}
	return summary
}

func normalizePublicCardDAVText(value string) string {
	var cleaned strings.Builder
	cleaned.Grow(len(value))
	for _, r := range strings.ToValidUTF8(value, "") {
		if unicode.IsSpace(r) {
			cleaned.WriteByte(' ')
			continue
		}
		if unicode.IsControl(r) {
			continue
		}
		cleaned.WriteRune(r)
	}
	return strings.Join(strings.Fields(cleaned.String()), " ")
}

func capPublicCardDAVText(value string) (string, bool) {
	if len(value) <= maxPublicCardDAVValueBytes {
		return value, false
	}
	end := maxPublicCardDAVValueBytes
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return value[:end], true
}

func publicConflictPropertyValue(version vcard.Version, property vcard.Property) (string, error) {
	name := strings.ToUpper(property.Name)
	if name != "ADR" && name != "N" && name != "ORG" {
		return cardDAVPropertyValue(version, property)
	}
	for _, parameter := range property.ParametersNamed("VALUE") {
		if len(parameter.Values) > 0 && !strings.EqualFold(parameter.Values[0].Decoded, "text") {
			return property.RawValue, nil
		}
	}
	raw, err := vcard.DecodeTextRawValue(property)
	if err != nil {
		return "", err
	}
	parts, err := vcard.SplitStructuredText(raw)
	if err != nil {
		return "", err
	}
	labels := map[string][]string{
		"ADR": {"PO box", "Extended address", "Street", "City", "Region", "Postal code", "Country"},
		"N":   {"Family", "Given", "Additional", "Prefix", "Suffix"},
		"ORG": {"Organization"},
	}[name]
	values := []string{}
	for index, part := range parts {
		if part == "" {
			continue
		}
		label := fmt.Sprintf("Component %d", index+1)
		if index < len(labels) {
			label = labels[index]
		} else if name == "ORG" {
			label = fmt.Sprintf("Unit %d", index)
		}
		values = append(values, label+": "+strconv.Quote(part))
	}
	return strings.Join(values, "; "), nil
}

func conflictChangedProperties(local, remote vcard.ResourceEnvelope) map[string]bool {
	properties := func(envelope vcard.ResourceEnvelope) map[string][]vcard.SemanticProperty {
		values := make(map[string][]vcard.SemanticProperty)
		for _, occurrence := range envelope.PropertyTree {
			name := strings.ToUpper(occurrence.Property.Name)
			switch name {
			case "BEGIN", "END", "VERSION", "UID", "PRODID", "REV", "CLIENTPIDMAP":
				continue
			}
			values[name] = append(values[name], vcard.NormalizeSemanticProperty(envelope.RenderMetadata.StoredVersion, occurrence.Property))
		}
		for _, group := range values {
			slices.SortFunc(group, vcard.CompareSemanticProperties)
		}
		return values
	}
	left, right := properties(local), properties(remote)
	changed := make(map[string]bool)
	for name := range right {
		if _, ok := left[name]; !ok {
			left[name] = nil
		}
	}
	for name, values := range left {
		if !slices.EqualFunc(values, right[name], func(a, b vcard.SemanticProperty) bool { return vcard.CompareSemanticProperties(a, b) == 0 }) {
			changed[name] = true
		}
	}
	return changed
}

func projectConflictComparison(localBody, remoteBody []byte, localDeleted, remoteDeleted bool) (ContactSummary, ContactSummary) {
	localEnvelope, localErr := vcard.ParseResourceEnvelope(localBody)
	remoteEnvelope, remoteErr := vcard.ParseResourceEnvelope(remoteBody)
	local, remote := emptyContactSummary(ConflictSideUnavailable), emptyContactSummary(ConflictSideUnavailable)
	if localDeleted {
		local.State = ConflictSideDeleted
	}
	if remoteDeleted {
		remote.State = ConflictSideDeleted
	}
	changed := map[string]bool{}
	if localErr == nil && remoteErr == nil && !localDeleted && !remoteDeleted {
		changed = conflictChangedProperties(localEnvelope, remoteEnvelope)
	}
	if localErr == nil && !localDeleted {
		local = projectConflictEnvelope(localEnvelope, changed)
	}
	if remoteErr == nil && !remoteDeleted {
		remote = projectConflictEnvelope(remoteEnvelope, changed)
	}
	projectConflictDifferenceMarkers(changed, &local, &remote)
	return local, remote
}

func projectConflictHiddenDifferences(localBody, remoteBody []byte, local, remote *ContactSummary) {
	left, err := vcard.ParseResourceEnvelope(localBody)
	if err != nil {
		return
	}
	right, err := vcard.ParseResourceEnvelope(remoteBody)
	if err != nil {
		return
	}
	projectConflictDifferenceMarkers(conflictChangedProperties(left, right), local, remote)
}

func projectConflictDifferenceMarkers(changed map[string]bool, local, remote *ContactSummary) {
	if local.State != ConflictSidePresent || remote.State != ConflictSidePresent {
		return
	}
	names := make([]string, 0, len(changed))
	for name := range changed {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, side := range []struct {
		summary *ContactSummary
		other   string
	}{{local, "remote"}, {remote, "local"}} {
		omitted := false
		for _, name := range names {
			shown := false
			for index := range side.summary.Fields {
				field := &side.summary.Fields[index]
				if field.Name != name {
					continue
				}
				shown = true
				if len(field.Labels) == maxPublicContactValues {
					field.Labels = field.Labels[:maxPublicContactValues-1]
					side.summary.Truncated = true
				}
				field.Labels = append(field.Labels, "Differs from "+side.other)
			}
			if shown {
				continue
			}
			if len(side.summary.Fields) >= maxPublicContactValues-1 {
				omitted = true
				continue
			}
			safeName := name
			switch name {
			case "FN", "N", "EMAIL", "TEL", "ADR", "ORG", "TITLE", "ROLE", "BDAY", "ANNIVERSARY", "CATEGORIES", "NOTE", "RELATED", "URL", "PHOTO", "IMPP", "NICKNAME", "LANG", "SOUND", "KEY":
			default:
				safeName = "OTHER"
			}
			side.summary.Fields = append(side.summary.Fields, ContactField{Name: safeName, Value: "Contents hidden", Labels: []string{"Differs from " + side.other}})
		}
		if len(names) > 0 && (omitted || side.summary.Truncated) {
			if len(side.summary.Fields) >= maxPublicContactValues {
				side.summary.Fields = side.summary.Fields[:maxPublicContactValues-1]
			}
			side.summary.Truncated = true
			side.summary.Fields = append(side.summary.Fields, ContactField{Name: "OTHER", Value: "Additional differences omitted", Labels: []string{"Differs from " + side.other}})
		}
	}
}
