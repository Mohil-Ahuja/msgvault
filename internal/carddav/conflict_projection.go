package carddav

import (
	"slices"
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
	summary := emptyContactSummary(ConflictSidePresent)
	emails := make(map[string]struct{})
	phones := make(map[string]struct{})
	for _, occurrence := range envelope.PropertyTree {
		property := occurrence.Property
		name := strings.ToUpper(property.Name)
		switch name {
		case "FN", "EMAIL", "TEL", "ADR", "N", "ORG", "TITLE", "ROLE", "BDAY", "ANNIVERSARY", "CATEGORIES":
		default:
			continue
		}
		value, err := cardDAVPropertyValue(envelope.RenderMetadata.StoredVersion, property)
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
		if len(labels) > 0 || (name != "FN" && name != "EMAIL" && name != "TEL") {
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

func projectConflictHiddenDifferences(localBody, remoteBody []byte, local, remote *ContactSummary) {
	if local.State != ConflictSidePresent || remote.State != ConflictSidePresent {
		return
	}
	localEnvelope, err := vcard.ParseResourceEnvelope(localBody)
	if err != nil {
		return
	}
	remoteEnvelope, err := vcard.ParseResourceEnvelope(remoteBody)
	if err != nil {
		return
	}
	properties := func(envelope vcard.ResourceEnvelope, name string) []vcard.SemanticProperty {
		values := []vcard.SemanticProperty{}
		for _, occurrence := range envelope.PropertyTree {
			if strings.EqualFold(occurrence.Property.Name, name) {
				values = append(values, vcard.NormalizeSemanticProperty(envelope.RenderMetadata.StoredVersion, occurrence.Property))
			}
		}
		slices.SortFunc(values, vcard.CompareSemanticProperties)
		return values
	}
	for _, name := range []string{"NOTE", "RELATED", "URL", "PHOTO"} {
		if slices.EqualFunc(properties(localEnvelope, name), properties(remoteEnvelope, name), func(a, b vcard.SemanticProperty) bool {
			return vcard.CompareSemanticProperties(a, b) == 0
		}) {
			continue
		}
		for _, side := range []struct {
			summary *ContactSummary
			other   string
		}{{local, "remote"}, {remote, "local"}} {
			if len(side.summary.Fields) == maxPublicContactValues {
				side.summary.Truncated = true
				continue
			}
			side.summary.Fields = append(side.summary.Fields, ContactField{Name: name, Value: "Contents hidden", Labels: []string{"Differs from " + side.other}})
		}
	}
}
