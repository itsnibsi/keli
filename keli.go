package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/PuerkitoBio/goquery"
)

type HourlyForecast struct {
	Hour                 string  `json:"hour"`
	WeatherSymbol        string  `json:"weather"`
	Temperature          float64 `json:"temperature"`
	TemperatureFeelsLike float64 `json:"temperatureFeelsLike"`
	WindSpeed            int     `json:"windSpeed"`
	Rainfall             float64 `json:"rainfall"`
	RainChance           int     `json:"rainChance"`
}

// WeatherData represents the weather data for a given city.
type WeatherData struct {
	// Human-readable name of the city we're looking at
	City string `json:"city"`
	// The hour the last observation update is from
	ObservationHour int `json:"observationHour"`
	// Text description of the weather
	WeatherSummary string `json:"weatherSummary"`
	// Current temperature (C)
	Temperature float64 `json:"temperature"`
	// How current temperature feels (C)
	TemperatureFeelsLike float64 `json:"temperatureFeelsLike"`
	// Today's min temperature (C)
	TemperatureMin float64 `json:"temperatureMin"`
	// Today's max temperature (C)
	TemperatureMax float64 `json:"temperatureMax"`
	// Amount of rain (mm)
	Rainfall float64 `json:"rainfall"`
	// Amount of snow (mm)
	Snowfall float64 `json:"snowfall"`
	// Wind speed (m/s)
	WindSpeed int `json:"windSpeed"`
	// Rain chance (%)
	RainChance int `json:"rainChance"`
	// Tomorrow's temperature (C)
	TemperatureTomorrow float64 `json:"temperatureTomorrow"`
	// Tomorrow's min temperature (C)
	TemperatureMinTomorrow float64 `json:"temperatureMinTomorrow"`
	// The time the sun rises
	Sunrise string `json:"sunrise"`
	// The time the sun sets
	Sunset string `json:"sunset"`
	// The length of the day (HH:MM)
	DayLength string `json:"dayLength"`
	// The last time the weather data was updated in the cache
	LastUpdated time.Time `json:"lastUpdated"`
	// Hourly forecast
	HourlyForecast []HourlyForecast `json:"hourlyForecast"`
}

// WeatherSource represents a source of weather data.
type WeatherSource struct {
	URL   string
	Parse func(*goquery.Document) (WeatherData, error)
}

var (
	cache         = make(map[string]WeatherData)
	cacheMutex    sync.Mutex
	cacheDuration = 5 * time.Minute

	weatherSources = []WeatherSource{
		{URL: "https://www.foreca.fi/Finland/", Parse: parseForecaData},
		{URL: "https://www.ampparit.com/saa/", Parse: parseAmpparitData},
		{URL: "http://www.moisio.fi/taivas/aurinko.php?paikka=", Parse: parseMoisioData},
	}
)

// GetWeatherData returns the weather data for the given city
func GetWeatherData(city string) (weather WeatherData, err error) {
	// clean up the city name of special characters
	city = sanitizeCityName(city)

	// cache check
	cacheMutex.Lock()
	cachedData, found := cache[city]
	cacheMutex.Unlock()
	if found && time.Since(cachedData.LastUpdated) < cacheDuration {
		return cachedData, nil
	}

	// channel for receiving partial weather data from sources
	weatherDataChan := make(chan WeatherData, len(weatherSources))

	// create a waitgroup to wait for all sources to finish parsing
	var wg sync.WaitGroup
	wg.Add(len(weatherSources))

	// fetch weather data from all sources
	for _, source := range weatherSources {
		go func(source WeatherSource) {
			defer wg.Done()

			url := source.URL + city

			// fetch the document
			res, err := http.Get(url)
			if err != nil {
				log.Printf("Error fetching data from %s: %v", url, err)
				return
			}
			defer res.Body.Close()

			// feed the document to goquery
			doc, err := goquery.NewDocumentFromReader(res.Body)
			if err != nil {
				log.Printf("Error parsing document from %s: %v", url, err)
				return
			}

			// Parse weather data from the document
			data, err := source.Parse(doc)
			if err != nil {
				log.Printf("Error parsing weather data from %s: %v", url, err)
				return
			}

			weatherDataChan <- data
		}(source)
	}

	// close channel after all sources have been parsed
	go func() {
		wg.Wait()
		close(weatherDataChan)
	}()

	// Collect parsed weather data
	var weatherData []WeatherData
	for data := range weatherDataChan {
		weatherData = append(weatherData, data)
		log.Printf("Found weather data for %s", city)
		log.Printf("Data: %+v", data)
	}

	finalWeatherData := mergeWeatherData(weatherData)
	finalWeatherData.LastUpdated = time.Now()

	if finalWeatherData.City == "" {
		return WeatherData{}, fmt.Errorf("No weather data found for city \"%s\"", city)
	}

	// Update the cache
	cacheMutex.Lock()
	cache[city] = finalWeatherData
	cacheMutex.Unlock()

	return finalWeatherData, nil
}

func sanitizeCityName(city string) string {
	replacer := strings.NewReplacer(
		"ä", "a",
		"ö", "o",
	)
	return replacer.Replace(city)
}

func mergeWeatherData(data []WeatherData) (md WeatherData) {
	chooseNonEmptyString := func(existing, incoming string) string {
		if existing != "" {
			return existing
		}
		return incoming
	}

	chooseNonZeroFloat64 := func(existing, incoming float64) float64 {
		if incoming != 0 {
			return incoming
		}
		return existing
	}

	for _, d := range data {
		// Foreca
		md.City = chooseNonEmptyString(md.City, d.City)
		md.TemperatureMax = chooseNonZeroFloat64(md.TemperatureMax, d.TemperatureMax)
		md.TemperatureMin = chooseNonZeroFloat64(md.TemperatureMin, d.TemperatureMin)
		md.Rainfall = chooseNonZeroFloat64(md.Rainfall, d.Rainfall)
		md.Snowfall = chooseNonZeroFloat64(md.Snowfall, d.Snowfall)
		if d.WindSpeed != 0 {
			md.WindSpeed = d.WindSpeed
		}
		md.WeatherSummary = chooseNonEmptyString(md.WeatherSummary, d.WeatherSummary)
		// Moisio
		md.Sunrise = chooseNonEmptyString(md.Sunrise, d.Sunrise)
		md.Sunset = chooseNonEmptyString(md.Sunset, d.Sunset)
		md.DayLength = chooseNonEmptyString(md.DayLength, d.DayLength)
		// Ampparit
		md.Temperature = chooseNonZeroFloat64(md.Temperature, d.Temperature)
		md.TemperatureFeelsLike = chooseNonZeroFloat64(md.TemperatureFeelsLike, d.TemperatureFeelsLike)
		if d.ObservationHour != 0 {
			md.ObservationHour = d.ObservationHour
		}
		md.TemperatureTomorrow = chooseNonZeroFloat64(md.TemperatureTomorrow, d.TemperatureTomorrow)
		md.TemperatureMinTomorrow = chooseNonZeroFloat64(md.TemperatureMinTomorrow, d.TemperatureMinTomorrow)
		if d.HourlyForecast != nil {
			md.HourlyForecast = d.HourlyForecast
			log.Printf("Hourly forecast: %v", d.HourlyForecast)
		}
	}

	return
}

func parseForecaData(doc *goquery.Document) (data WeatherData, err error) {
	// Temperature max
	tempMaxText := doc.Find("#dailybox > div:nth-child(1) > a > div > p.tx > abbr").First().Text()
	tempMax, err := cleanTemperatureString(tempMaxText)
	if err != nil {
		log.Printf("Foreca - Error parsing temperature: %v", err)
		return WeatherData{}, err
	}
	data.TemperatureMax = tempMax

	// Temperature min
	tempMinText := doc.Find("#dailybox > div:nth-child(1) > a > div > p.tn > abbr").First().Text()
	tempMin, err := cleanTemperatureString(tempMinText)
	if err != nil {
		log.Printf("Foreca - Error parsing temperature FL: %v", err)
		return WeatherData{}, err
	}
	data.TemperatureMin = tempMin

	// Wind speed
	windSpeedText := doc.Find("#dailybox > div:nth-child(1) > a > div > p.w > span > em").First().Text()
	windSpeed, err := strconv.Atoi(windSpeedText)
	if err != nil {
		log.Printf("Foreca - Error parsing wind speed: %v", err)
		return WeatherData{}, err
	}
	data.WindSpeed = windSpeed

	// // Snowfall
	// snowfallText := doc.Find("#dailybox > div:nth-child(1) > a > div > div.p > em").First().Text()
	// snowfall, err := strconv.ParseFloat(strings.Replace(snowfallText, ",", ".", -1), 64)
	// if err != nil {
	// 	log.Printf("Foreca - Error parsing snowfall: %v", err)
	// 	return WeatherData{}, err
	// }
	// data.Snowfall = snowfall

	// Weather summarized text
	weatherSummary := doc.Find(".today .day .txt").First().Text()
	data.WeatherSummary = strings.Split(weatherSummary, ".")[0]

	return
}

func parseAmpparitData(doc *goquery.Document) (data WeatherData, err error) {
	// Parse the city name from the document title
	city := doc.Find(".current-weather__location").Text()
	if city == "" {
		return WeatherData{}, errors.New("failed to parse city name")
	}
	data.City = city

	temperatureText := doc.Find("span.current-weather__temperature").First().Text()
	temperature, err := cleanTemperatureString(temperatureText)
	if err != nil {
		return WeatherData{}, err
	}
	data.Temperature = temperature

	temperatureFeelsLikeText := doc.Find("span.weather-lighter.weather-temperature-feelslike").First().Text()
	temperatureFeelsLike, err := cleanTemperatureString(temperatureFeelsLikeText)
	if err != nil {
		return WeatherData{}, err
	}
	data.TemperatureFeelsLike = temperatureFeelsLike

	// Rainfall amount
	rainfallText := doc.Find(".current-weather__precipitation .weather-value").First().Text()
	rainfallText = strings.Replace(rainfallText, " mm", "", -1)
	rainfall, err := strconv.ParseFloat(rainfallText, 64)
	if err != nil {
		return WeatherData{}, err
	}
	data.Rainfall = rainfall

	// Updated hour
	observationHour := doc.Find("ol > li:nth-child(1) > div.weather-time > time").First().Text()
	observationHourInt, err := strconv.Atoi(observationHour)
	if err != nil {
		return WeatherData{}, err
	}
	data.ObservationHour = observationHourInt

	hours := doc.Find(".weather-hour-selector ol > li").Slice(0, 24)
	hours.Each(func(i int, s *goquery.Selection) {
		tempString := s.Find(".weather-temperature > span").First().Text()
		temp, err := cleanTemperatureString(tempString)
		if err != nil {
			log.Printf("Ampparit - Error parsing hourly temperature: %v", err)
			return
		}

		tempFLString := s.Find(".weather-temperature > span").First().Text()
		tempFL, err := cleanTemperatureString(tempFLString)
		if err != nil {
			log.Printf("Ampparit - Error parsing hourly temperature FL: %v", err)
			return
		}

		windSpeedStr := s.Find(".weather-wind > .weather-value").First().Text()
		windSpeed, err := strconv.Atoi(windSpeedStr)
		if err != nil {
			log.Printf("Ampparit - Error parsing hourly wind speed: %v", err)
			return
		}

		rainfallStr := s.Find(".weather-precipitation-amount").First().Text()
		rainfallStr = strings.Replace(rainfallStr, " mm", "", -1)
		rainfall, err := strconv.ParseFloat(rainfallStr, 64)
		if err != nil {
			log.Printf("Ampparit - Error parsing hourly rainfall: %v", err)
			return
		}

		weatherSymbolText := s.Find(".weather-symbol > span").First().AttrOr("class", "invalid")
		var weatherSymbol string

		switch weatherSymbolText {
		case "d000":
			weatherSymbol = "☀️"
		case "n000":
			weatherSymbol = "🌜"
		default:
			weatherSymbol = "❓"
		}

		data.HourlyForecast = append(data.HourlyForecast, HourlyForecast{
			Hour:                 s.Find("time").Text(),
			WeatherSymbol:        weatherSymbol,
			Temperature:          temp,
			TemperatureFeelsLike: tempFL,
			WindSpeed:            windSpeed,
			Rainfall:             rainfall,
			RainChance:           0,
		})
	})

	// Tomorrow weather
	temperatureTomorrowText := doc.Find(".weekly-weather-list-wrapper:nth-child(2) .weather-temperature").First().Text()
	temperatureTomorrow, err := cleanTemperatureString(temperatureTomorrowText)
	if err != nil {
		return WeatherData{}, err
	}
	data.TemperatureTomorrow = temperatureTomorrow

	temperatureTomorrowMinText := doc.Find(".weekly-weather-list-wrapper:nth-child(2) .weather-min-temperature").First().Text()
	temperatureTomorrowMinText = strings.Replace(temperatureTomorrowMinText, "alin ", "", -1)
	temperatureTomorrowMin, err := cleanTemperatureString(temperatureTomorrowMinText)
	if err != nil {
		return WeatherData{}, err
	}
	data.TemperatureMinTomorrow = temperatureTomorrowMin

	data.WeatherSummary = ""

	return
}

func parseMoisioData(doc *goquery.Document) (data WeatherData, err error) {
	data.Sunrise = doc.Find("td.tbl0:nth-child(4)").First().Text()
	data.Sunset = doc.Find("td.tbl0:nth-child(5)").First().Text()
	data.DayLength = doc.Find("td.tbl0:nth-child(6)").First().Text()
	return
}

func cleanTemperatureString(temperature string) (temp float64, err error) {
	parser := strings.NewReplacer(
		"°", "",
		"C", "",
		"F", "",
		",", ".",
	)

	temperature = parser.Replace(temperature)
	temperature = strings.TrimSpace(temperature)

	temperatureFloat, err := strconv.ParseFloat(temperature, 64)
	if err != nil {
		log.Printf("Error parsing temperature: %v", err)
		return 0, err
	}
	return temperatureFloat, nil
}

func weatherHandler(w http.ResponseWriter, r *http.Request) {
	log.Printf("Received request for %s", r.URL.Path)

	city := r.URL.Query().Get("city")
	if city == "" {
		http.Error(w, "Missing 'city' parameter", http.StatusBadRequest)
		return
	}

	weather, err := GetWeatherData(city)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)

	format := r.URL.Query().Get("format")
	switch format {
	case "text":
		weatherTextHandler(w, weather)
	default:
		weatherJSONHandler(w, weather)
	}
}

func weatherTextHandler(w http.ResponseWriter, weather WeatherData) {
	w.Header().Set("Content-Type", "text/plain")

	output := fmt.Sprintf("Sää %s (Klo. %02d)\n", weather.City, weather.ObservationHour)
	output += fmt.Sprintf("%s\n\n", weather.WeatherSummary)

	output += fmt.Sprintf("Lämpötila: %s (Tuntuu kuin %s)\n", temperatureWithSign(weather.Temperature), temperatureWithSign(weather.TemperatureFeelsLike))
	output += fmt.Sprintf("Päivän alin: %s\n", temperatureWithSign(weather.TemperatureMin))
	output += fmt.Sprintf("Päivän ylin: %s\n", temperatureWithSign(weather.TemperatureMin))

	output += fmt.Sprintf("Sadetta: %.1f mm\n", weather.Rainfall)
	output += fmt.Sprintf("Lunta: %.1f cm\n", weather.Snowfall)
	output += fmt.Sprintf("Tuuli: %d m/s\n", weather.WindSpeed)

	output += fmt.Sprintf("Huomenna: %s (Alin: %s)\n", temperatureWithSign(weather.TemperatureTomorrow), temperatureWithSign(weather.TemperatureMinTomorrow))

	output += fmt.Sprintf("Auringonnousu: %s\nAuringonlasku: %s\n", weather.Sunrise, weather.Sunset)
	output += fmt.Sprintf("Päivän pituus: %s\n", weather.DayLength)

	_, err := w.Write([]byte(output))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func temperatureWithSign(temperature float64) string {
	if temperature > 0 {
		return fmt.Sprintf("+%.1f°C", temperature)
	}
	return fmt.Sprintf("%.1f°C", temperature)
}

func weatherJSONHandler(w http.ResponseWriter, weather WeatherData) {
	jsonData, err := json.Marshal(weather)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	_, err = w.Write(jsonData)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func weatherPageHandler(w http.ResponseWriter, r *http.Request) {
	log.Printf("Received request for %s", r.URL.Path)

	w.Header().Set("Content-Type", "text/html")

	city := r.URL.Path[1:]

	if city == "" {
		city = "Hyvinkää"
	}

	weather, err := GetWeatherData(city)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)

	// Similar template but using a weather-app type styling using tailwindcss
	tmpl, err := template.ParseFiles("templates/weather.html")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	err = tmpl.Execute(w, weather)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func placesHandler(w http.ResponseWriter, r *http.Request) {
	log.Printf("Received request for %s", r.URL.Path)

	w.Header().Set("Content-Type", "text/json")

	places, err := GetPlaces()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(places)
}

// GetPlaces returns a list of known places
func GetPlaces() (places []string, err error) {
	file, err := os.Open("data/places.txt")
	if err != nil {
		return nil, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		places = append(places, scanner.Text())
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return places, nil
}

func main() {
	http.HandleFunc("/", weatherPageHandler)
	http.HandleFunc("/w", weatherHandler)
	http.HandleFunc("/api", weatherHandler)
	http.HandleFunc("/places", placesHandler)
	http.HandleFunc("/smoke", smokeHandler)

	log.Printf("weather balloon spying on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}

func smokeHandler(w http.ResponseWriter, r *http.Request) {
	loc, _ := time.LoadLocation("Europe/Helsinki")
	time.Local = loc
	quitSmokingTime := time.Date(2024, time.April, 21, 18, 20, 0, 0, time.Local)
	timeSinceQuitSmoking := time.Since(quitSmokingTime)
	days := int(timeSinceQuitSmoking.Hours())/24 + int(timeSinceQuitSmoking.Minutes())/1440
	hours := int(timeSinceQuitSmoking.Hours())%24 + int(timeSinceQuitSmoking.Minutes())%60/60
	minutes := int(timeSinceQuitSmoking.Minutes()) % 60
	seconds := int(timeSinceQuitSmoking.Seconds()) % 60

	fmt.Fprintf(w, "%d days %d hours %d minutes %d seconds", days, hours, minutes, seconds)
}
