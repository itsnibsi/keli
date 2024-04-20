package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/PuerkitoBio/goquery"
)

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
	}

	return
}

func parseForecaData(doc *goquery.Document) (data WeatherData, err error) {
	// Parse the city name from the document title
	city := strings.TrimSuffix(strings.TrimPrefix(doc.Find("title").Text(), "Täsmäsää "), " - Foreca.fi")
	if city == "" {
		return WeatherData{}, errors.New("failed to parse city name")
	}
	data.City = city

	// Temperature max
	tempMaxText := doc.Find("#dailybox > div:nth-child(1) > a > div > p.tx > abbr").First().Text()
	tempMax, err := cleanTemperatureString(tempMaxText)
	if err != nil {
		return WeatherData{}, err
	}
	data.TemperatureMax = tempMax

	// Temperature min
	tempMinText := doc.Find("#dailybox > div:nth-child(1) > a > div > p.tn > abbr").First().Text()
	tempMin, err := cleanTemperatureString(tempMinText)
	if err != nil {
		return WeatherData{}, err
	}
	data.TemperatureMin = tempMin

	// Wind speed
	windSpeedText := doc.Find("#dailybox > div:nth-child(1) > a > div > p.w > span > em").First().Text()
	windSpeed, err := strconv.Atoi(windSpeedText)
	if err != nil {
		return WeatherData{}, err
	}
	data.WindSpeed = windSpeed

	// Snowfall
	snowfallText := doc.Find("#dailybox > div:nth-child(1) > a > div > div.p > em").First().Text()
	snowfall, err := strconv.ParseFloat(strings.Replace(snowfallText, ",", ".", -1), 64)
	if err != nil {
		return WeatherData{}, err
	}
	data.Snowfall = snowfall

	// Weather summarized text
	weatherSummary := doc.Find("p.txt").First().Text()
	data.WeatherSummary = strings.Split(weatherSummary, ".")[0]

	return
}

func parseAmpparitData(doc *goquery.Document) (data WeatherData, err error) {
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

	// Create minimal html template with title and styles
	// 	tmpl	 := template.Must(template.New("weather").Parse(`
	// <!DOCTYPE html>
	// <html>
	// <head>
	// 	<meta charset="utf-8">
	// 	<title>Weather Balloon</title>
	// 	<link rel="stylesheet" href="https://cdnjs.cloudflare.com/ajax/libs/semantic-ui/2.4.1/semantic.min.css">
	// </head>
	// <body>
	// 	<div class="ui container">
	// 		<div class="ui segment">
	// 			<h1>Sää {{.City}} (Klo. {{.ObservationHour}})</h1>
	// 			<h2>{{.WeatherSummary}}</h2>
	// 			<h2>{{.Temperature}}°C (Tuntuu kuin {{.TemperatureFeelsLike}}°C)</h2>
	// 			<p>Päivän alin: {{.TemperatureMin}}°C</p>
	// 			<p>Päivän ylin: {{.TemperatureMax}}°C</p>
	// 			<p>Sadetta: {{.Rainfall}} mm</p>
	// 			<p>Lunta: {{.Snowfall}} cm</p>
	// 			<p>Tuuli: {{.WindSpeed}} m/s</p>
	// 			<p>Huomenna: {{.TemperatureTomorrow}}°C (Alin: {{.TemperatureMinTomorrow}}°C)</p>
	// 			<p>Auringonnousu: {{.Sunrise}}</p>
	// 			<p>Auringonlasku: {{.Sunset}}</p>
	// 			<p>Päivän pituus: {{.DayLength}}</p>
	// 		</div>
	// 	</div>
	// </body>
	// </html>
	// `))

	// Similar template but using a weather-app type styling using tailwindcss
	tailwindTmpl := template.Must(template.New("weather-tailwind").Parse(`
<!DOCTYPE html>
<html>
<head>
	<meta charset="utf-8">
	<title>Sää {{.City}}</title>
	<meta name="viewport" content="width=device-width, initial-scale=1">
	<link rel="stylesheet" href="https://unpkg.com/tailwindcss@^1.0/dist/tailwind.min.css">
	<script src="https://kit.fontawesome.com/ab6199b688.js" crossorigin="anonymous"></script>
</head>
<body class="bg-gray-200">
	<div class="container mx-auto p-4">
		<div class="text-center">
			<h1 class="text-3xl text-gray-900 font-bold uppercase">{{.City}} (Klo {{.ObservationHour}})</h1>
		</div>

		<div class="p-8 bg-white shadow-md rounded-lg mt-4">
			<h2 class="text-3xl text-gray-800 text-center pb-8">{{.WeatherSummary}}</h2>
			<div class="flex justify-between items-center">
				<div class="text-center">
					<p class="text-4xl font-bold text-gray-900">{{.Temperature}}°C</p>
				</div>
				<div>
					<p class="text-2xl font-bold text-gray-700">Tuntuu {{.TemperatureFeelsLike}}°C</p>
				</div>
			</div>
			<div class="bg-gray-100 mt-8 p-4 rounded-lg shadow-sm grid grid-cols-5 gap-4 items-center justify-center">
				<div class="flex items-center justify-center gap-2">
					<div class="flex flex-col items-center justify-center">
						<i class="fas fa-thermometer-full text-red-600 text-2xl"></i>
						<div class="text-xl font-semibold text-red-600">{{.TemperatureMax}}°C</div>
					</div>
				</div>
				<div class="flex items-center justify-center gap-2">
					<div class="flex flex-col items-center justify-center">
						<i class="fas fa-thermometer-empty text-blue-600 text-2xl"></i>
						<div class="text-xl font-semibold text-blue-600">{{.TemperatureMin}}°C</div>
					</div>
				</div>
				<div class="flex items-center justify-center gap-2">
					<div class="flex flex-col items-center justify-center">
						<i class="fas fa-tint text-blue-500 text-2xl"></i>
						<div class="text-xl font-semibold text-blue-500">{{.Rainfall}} mm</div>
					</div>
				</div>
				<div class="flex items-center justify-center">
					<div class="flex flex-col items-center justify-center">
						<i class="fas fa-snowflake text-indigo-500 text-2xl"></i>
						<div class="text-xl font-semibold text-indigo-500">{{.Snowfall}} cm</div>
					</div>
				</div>
				<div class="flex items-center justify-center">
					<div class="flex flex-col items-center justify-center">
						<i class="fas fa-wind text-gray-600 text-2xl"></i>
						<div class="text-xl font-semibold text-gray-600">{{.WindSpeed}} m/s</div>
					</div>
				</div>
			</div>
		</div>

		<div class="bg-white shadow-md rounded-lg mt-4 p-8 grid grid-cols-1 gap-4">
			<h2 class="text-3xl text-gray-900 font-bold text-center">Huomenna</h2>
			<div class="flex justify-between items-center">
				<div class="text-center">
					<p class="text-6xl font-bold text-gray-900">{{.TemperatureTomorrow}}°C</p>
				</div>
				<div>
					<p class="text-3xl font-bold text-gray-700">Alin: {{.TemperatureMinTomorrow}}°C</p>
				</div>
			</div>
		</div>

		<div class="bg-white shadow-md rounded-lg mt-4 p-8 grid grid-cols-1 gap-4">
			<h2 class="text-3xl text-gray-900 font-bold pb-8 text-center">Auringon nousu ja lasku</h2>
			<div class="grid grid-cols-2 gap-4 items-center">
				<div class="flex items-center justify-center">
					<div class="flex flex-col items-center justify-center bg-gradient-to-br from-orange-600 to-orange-500 text-transparent bg-clip-text">
						<i class="fas fa-sun text-4xl"></i>
						<p class="font-bold text-2xl">Nousee {{.Sunrise}}</p>
					</div>
				</div>
				<div class="flex items-center justify-center">
					<div class="flex flex-col items-center justify-center bg-gradient-to-br from-purple-500 to-purple-700 text-transparent bg-clip-text">
						<i class="fas fa-sun text-4xl"></i>
						<p class="font-bold text-2xl">Laskee {{.Sunset}}</p>
					</div>
				</div>
			</div>
		</div>
	</div>
</body>
</html>
`))

	err = tailwindTmpl.Execute(w, weather)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func main() {
	http.HandleFunc("/", weatherPageHandler)
	http.HandleFunc("/w", weatherHandler)
	http.HandleFunc("/api", weatherHandler)

	log.Printf("weather balloon spying on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
