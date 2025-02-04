package main

import (
	"encoding/json"
	"fmt"
	"github.com/arran4/golang-ical"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Event represents a calendar event to be returned in the JSON response.
type Event struct {
	UID         string    `json:"uid"`
	Summary     string    `json:"summary"`
	Description string    `json:"description"`
	Location    string    `json:"location"`
	Start       time.Time `json:"start"`
	End         time.Time `json:"end"`
	AllDay      bool      `json:"all_day"`
}

// caljsonHandler handles the /caljson endpoint.
func caljsonHandler(w http.ResponseWriter, r *http.Request) {
	// Parse query parameters
	params := r.URL.Query()
	icsURL := params.Get("ics")
	dayStr := params.Get("day")

	// Validate query parameters
	if icsURL == "" {
		http.Error(w, "Missing 'ics' parameter", http.StatusBadRequest)
		return
	}

	if dayStr == "" {
		dayStr = "0"
	}
	dayOffset, err := strconv.Atoi(dayStr)
	if err != nil {
		http.Error(w, "Invalid 'day' parameter", http.StatusBadRequest)
		return
	}

	// Decode the ICS URL
	decodedIcsURL, err := url.QueryUnescape(icsURL)
	if err != nil {
		http.Error(w, "Invalid 'ics' URL", http.StatusBadRequest)
		return
	}

	// log that we recieved a request, ip address, and the ics url
	fmt.Println("Request from: ", r.RemoteAddr, " for ", decodedIcsURL)

	// Fetch the ICS data
	resp, err := http.Get(decodedIcsURL)
	if err != nil {
		http.Error(w, "Failed to fetch ICS URL", http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		http.Error(w, "Failed to retrieve ICS file", http.StatusInternalServerError)
		return
	}

	icsData, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, "Failed to read ICS data", http.StatusInternalServerError)
		return
	}

	// Parse the ICS data
	calendar, err := ics.ParseCalendar(strings.NewReader(string(icsData)))
	if err != nil {
		http.Error(w, "Failed to parse ICS data", http.StatusInternalServerError)
		return
	}

	// Determine the target date (start and end of the day)
	now := time.Now()
	targetDate := now.AddDate(0, 0, dayOffset)
	targetStart := time.Date(targetDate.Year(), targetDate.Month(), targetDate.Day(), 0, 0, 0, 0, time.Local)
	targetEnd := targetStart.Add(24 * time.Hour)

	// Collect events on the target date
	var events []Event
	for _, component := range calendar.Components {
		if vevent, ok := component.(*ics.VEvent); ok {
			event, err := parseEvent(vevent, targetStart, targetEnd)
			if err != nil {
				continue
			}
			if event != nil {
				events = append(events, *event)
			}
		}
	}

	// Sort events by start time
	sort.Slice(events, func(i, j int) bool {
		return events[i].Start.Before(events[j].Start)
	})

	// Return events as JSON
	w.Header().Set("Content-Type", "application/json")
	err = json.NewEncoder(w).Encode(events)
	if err != nil {
		http.Error(w, "Failed to encode events to JSON", http.StatusInternalServerError)
		return
	}
}

// parseEvent extracts event details and checks if it occurs on the target date.
func parseEvent(vevent *ics.VEvent, targetStart, targetEnd time.Time) (*Event, error) {
	// Get event properties
	startProp := vevent.GetProperty(ics.ComponentPropertyDtStart)
	endProp := vevent.GetProperty(ics.ComponentPropertyDtEnd)
	summaryProp := vevent.GetProperty(ics.ComponentPropertySummary)
	descriptionProp := vevent.GetProperty(ics.ComponentPropertyDescription)
	locationProp := vevent.GetProperty(ics.ComponentPropertyLocation)
	uidProp := vevent.GetProperty(ics.ComponentPropertyUniqueId)

	// Parse start and end times
	var (
		err        error
		start, end time.Time
		allDay     bool
	)

	if startProp == nil {
		return nil, fmt.Errorf("event missing DTSTART")
	}

	// Check if the event is all-day
	valueParam := startProp.ICalParameters["VALUE"]
	if len(valueParam) > 0 && valueParam[0] == "DATE" {
		// All-day event
		allDay = true
		start, err = time.Parse("20060102", startProp.Value)
		if err != nil {
			return nil, err
		}
		// DTEND is exclusive; adjust end date
		if endProp != nil {
			end, err = time.Parse("20060102", endProp.Value)
			if err != nil {
				return nil, err
			}
		} else {
			// If DTEND is missing, assume one-day event
			end = start.Add(24 * time.Hour)
		}
	} else {
		// Timed event
		start, err = parseICalTime(startProp.Value, startProp)
		if err != nil {
			return nil, err
		}
		if endProp != nil {
			end, err = parseICalTime(endProp.Value, endProp)
			if err != nil {
				return nil, err
			}
		} else {
			// If DTEND is missing, assume zero-duration event
			end = start
		}
	}

	// Adjust for time zones if TZID is present
	if tzidVals, ok := startProp.ICalParameters["TZID"]; ok {
		if len(tzidVals) > 0 {
			loc, err := time.LoadLocation(tzidVals[0])
			if err == nil {
				start = start.In(loc)
			}
		}
	}

	if endProp != nil {
		if tzidVals, ok := endProp.ICalParameters["TZID"]; ok {
			if len(tzidVals) > 0 {
				loc, err := time.LoadLocation(tzidVals[0])
				if err == nil {
					end = end.In(loc)
				}
			}
		}
	}

	// Check if the event occurs on the target date
	if end.After(targetStart) && start.Before(targetEnd) {
		return &Event{
			UID:         uidProp.Value,
			Summary:     summaryProp.Value,
			Description: descriptionProp.Value,
			Location:    locationProp.Value,
			Start:       start,
			End:         end,
			AllDay:      allDay,
		}, nil
	}

	return nil, nil // Event not on target date
}

// parseICalTime parses an iCalendar date-time string into a time.Time, considering time zones.
func parseICalTime(value string, prop *ics.IANAProperty) (time.Time, error) {
	format := "20060102T150405Z0700"
	if strings.HasSuffix(value, "Z") {
		format = "20060102T150405Z"
		return time.Parse(format, value)
	}

	// Check for TZID
	loc := time.Local
	if tzidVals, ok := prop.ICalParameters["TZID"]; ok {
		if len(tzidVals) > 0 {
			var err error
			loc, err = time.LoadLocation(tzidVals[0])
			if err != nil {
				return time.Time{}, err
			}
		}
	}

	return time.ParseInLocation("20060102T150405", value, loc)
}

func main() {
	http.HandleFunc("/caljson", caljsonHandler)
	fmt.Println("Server is listening on port 8080...")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
