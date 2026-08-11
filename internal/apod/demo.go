package apod

import (
	"context"
	"fmt"
	"math/rand/v2"
	"time"
)

// DemoClient serves a small built-in set of public-domain NASA images
// instead of calling the APOD API. It exists because NASA's APOD API has
// been returning placeholder content (see IsPlaceholder); with DEMO_MODE=1
// every feature can still be exercised and demonstrated.
//
// Each date maps deterministically to one sample, so deep links, ranges and
// "on this day" behave consistently. Every entry is flagged Sample so the UI
// labels it, and the server never stores it as real APOD data.
type DemoClient struct {
	// Now returns the current time; defaults to time.Now.
	Now func() time.Time
}

// demoEntry is one sample. The images and video are from NASA's Image and
// Video Library (images.nasa.gov) and are public domain; the captions are
// our own short descriptions.
type demoEntry struct {
	title, explanation, url, hdURL, mediaType, thumbnail string
}

const demoAttribution = "NASA (public domain), via images.nasa.gov. Sample data."

var demoEntries = []demoEntry{
	{
		title:       "Pillars of Creation",
		explanation: "Towering columns of cold gas and dust in the Eagle Nebula, about 6,500 light-years away. Inside them, gravity is gathering material into new stars, while radiation from nearby young stars slowly erodes the pillars.",
		url:         "https://images-assets.nasa.gov/image/GSFC_20171208_Archive_e000842/GSFC_20171208_Archive_e000842~medium.jpg",
		hdURL:       "https://images-assets.nasa.gov/image/GSFC_20171208_Archive_e000842/GSFC_20171208_Archive_e000842~orig.jpg",
		mediaType:   "image",
	},
	{
		title:       "The Cosmic Cliffs of Carina",
		explanation: "The edge of a star-forming region in the Carina Nebula, seen in infrared by the James Webb Space Telescope. Infrared light passes through dust, revealing young stars that are hidden at visible wavelengths.",
		url:         "https://images-assets.nasa.gov/image/carina_nebula/carina_nebula~medium.jpg",
		hdURL:       "https://images-assets.nasa.gov/image/carina_nebula/carina_nebula~large.jpg",
		mediaType:   "image",
	},
	{
		title:       "The Andromeda Galaxy",
		explanation: "Our nearest large galactic neighbour, about 2.5 million light-years away. Andromeda is a spiral galaxy somewhat larger than the Milky Way, and the two are expected to merge in a few billion years.",
		url:         "https://images-assets.nasa.gov/image/PIA04921/PIA04921~medium.jpg",
		hdURL:       "https://images-assets.nasa.gov/image/PIA04921/PIA04921~orig.jpg",
		mediaType:   "image",
	},
	{
		title:       "The Crab Nebula",
		explanation: "The expanding remains of a star whose explosion was recorded by astronomers in 1054. At its heart spins a pulsar, a city-sized neutron star rotating about 30 times per second, which powers the nebula's glow.",
		url:         "https://images-assets.nasa.gov/image/PIA03606/PIA03606~medium.jpg",
		hdURL:       "https://images-assets.nasa.gov/image/PIA03606/PIA03606~orig.jpg",
		mediaType:   "image",
	},
	{
		title:       "Total Solar Eclipse of 2017",
		explanation: "The Moon's shadow sweeps across the United States during the total solar eclipse of 21 August 2017, seen from a million miles away by NASA's EPIC camera aboard the DSCOVR spacecraft.",
		url:         "https://images-assets.nasa.gov/video/GSFC_20170822_Total_m12690_Eclipse/GSFC_20170822_Total_m12690_Eclipse~small.mp4",
		mediaType:   "video",
		thumbnail:   "https://images-assets.nasa.gov/video/GSFC_20170822_Total_m12690_Eclipse/GSFC_20170822_Total_m12690_Eclipse~medium_1.jpg",
	},
	{
		title:       "Toward the Heart of the Milky Way",
		explanation: "A crowded field of stars toward the centre of our galaxy. Thick dust hides this region from visible-light telescopes, so views like this rely on infrared light, which passes through the dust.",
		url:         "https://images-assets.nasa.gov/image/PIA13932/PIA13932~medium.jpg",
		hdURL:       "https://images-assets.nasa.gov/image/PIA13932/PIA13932~orig.jpg",
		mediaType:   "image",
	},
}

var firstDay = time.Date(1995, time.June, 16, 0, 0, 0, 0, time.UTC)

// Fetch answers q from the sample set, mirroring the real API's behaviour
// for each query form.
func (d *DemoClient) Fetch(_ context.Context, q Query) ([]Item, error) {
	now := time.Now
	if d.Now != nil {
		now = d.Now
	}
	y, m, day := now().UTC().Date()
	today := time.Date(y, m, day, 0, 0, 0, 0, time.UTC)

	switch {
	case q.IsRange():
		start, err1 := time.Parse(time.DateOnly, q.StartDate)
		end, err2 := time.Parse(time.DateOnly, q.EndDate)
		if err1 != nil || err2 != nil || end.Before(start) {
			return nil, fmt.Errorf("apod demo: invalid range %q..%q", q.StartDate, q.EndDate)
		}
		var items []Item
		for t := start; !t.After(end); t = t.AddDate(0, 0, 1) {
			items = append(items, demoItem(t))
		}
		return items, nil

	case q.Date != "":
		t, err := time.Parse(time.DateOnly, q.Date)
		if err != nil {
			return nil, fmt.Errorf("apod demo: invalid date %q", q.Date)
		}
		return []Item{demoItem(t)}, nil

	case q.Count > 0:
		// Random days across the archive, like the real ?count= query.
		span := int(today.Sub(firstDay).Hours() / 24)
		items := make([]Item, q.Count)
		for i := range items {
			items[i] = demoItem(firstDay.AddDate(0, 0, rand.IntN(span+1))) //nolint:gosec // G404: random sample dates, not a security decision
		}
		return items, nil

	default:
		return []Item{demoItem(today)}, nil
	}
}

// demoItem returns the sample for a date; the same date always gets the
// same sample.
func demoItem(t time.Time) Item {
	days := int(t.Sub(firstDay).Hours() / 24)
	e := demoEntries[((days%len(demoEntries))+len(demoEntries))%len(demoEntries)]
	return Item{
		Date:         t.Format(time.DateOnly),
		Title:        e.title,
		Explanation:  e.explanation,
		URL:          e.url,
		HDURL:        e.hdURL,
		MediaType:    e.mediaType,
		ThumbnailURL: e.thumbnail,
		Copyright:    demoAttribution,
		Sample:       true,
	}
}
