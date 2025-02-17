package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/arran4/golang-ical"
	"github.com/gin-gonic/gin"
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
func caljsonHandler(c *gin.Context) {
	// Parse query parameters
	icsURL := c.Query("ics")
	dayStr := c.DefaultQuery("day", "0")

	// Validate query parameters
	if icsURL == "" {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "Missing 'ics' parameter"})
		return
	}

	dayOffset, err := strconv.Atoi(dayStr)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "Invalid 'day' parameter"})
		return
	}

	// Decode the ICS URL
	decodedIcsURL, err := url.QueryUnescape(icsURL)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "Invalid 'ics' URL"})
		return
	}

	// Log the request details
	fmt.Println("Request from:", c.ClientIP(), "for", decodedIcsURL)

	// Fetch the ICS data
	resp, err := http.Get(decodedIcsURL)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch ICS URL"})
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve ICS file"})
		return
	}

	icsData, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Failed to read ICS data"})
		return
	}

	// Parse the ICS data
	calendar, err := ics.ParseCalendar(strings.NewReader(string(icsData)))
	if err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Failed to parse ICS data"})
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
	c.JSON(http.StatusOK, events)
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
	if strings.HasSuffix(value, "Z") {
		// UTC time
		return time.Parse("20060102T150405Z", value)
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
	r := gin.Default()

	// Define the /caljson route
	r.GET("/caljson", caljsonHandler)

	fmt.Println("Server is listening on port 8030...")
	if err := r.Run(":8030"); err != nil {
		log.Fatal(err)
	}
}
