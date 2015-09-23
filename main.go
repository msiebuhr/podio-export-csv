package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/andreas/podio-go"
	"github.com/bgentry/speakeasy"
)

var (
	orgName   string
	spaceName string
	appName   string

	username string

	limit int
)

func init() {
	flag.StringVar(&orgName, "org", "", "Organization")
	flag.StringVar(&spaceName, "space", "", "Space")
	flag.StringVar(&appName, "app", "", "App")
	flag.StringVar(&username, "username", "", "Username")

	flag.IntVar(&limit, "limit", 1e6, "Max number of items to fetch")
}

func main() {
	flag.Parse()

	password, err := speakeasy.FAsk(os.Stderr, fmt.Sprintf("Plese enter password for `%s`: ", username))
	if err != nil {
		fmt.Println("Error: ", err)
		return
	}

	authToken, err := podio.AuthWithUserCredentials(
		"podio-export-csv",
		"YbtU41kuIGdF5FBZs7lLx0itvAPVv25SWCsZgNIDpwrPmmeFOhm28NBf5ePBrwAS",
		username,
		password,
	)
	if err != nil {
		fmt.Println("Auth failed: ", err)
		return
	}

	client := podio.NewClient(authToken)

	// Get the right org
	orgs, err := client.GetOrganizations()
	var org podio.Organization

	if err != nil {
		fmt.Println("Failed to get orgs: ", err)
		return
	}

	for _, o := range orgs {
		if o.Name == orgName {
			org = o
			break
		}
	}

	if org.Id == 0 {
		fmt.Printf("Could not find organization %s in %s\n", orgName, orgs)
		return
	}

	// Then look for workspaces
	spaces, err := client.GetSpaces(org.Id)
	var space podio.Space
	if err != nil {
		fmt.Println("Failed to get spaces: ", err)
		return
	}

	for _, s := range spaces {
		if s.Name == spaceName {
			space = s
			break
		}
	}

	if space.Id == 0 {
		fmt.Println("Could not find workspace", spaceName)
		return
	}

	// Find app
	apps, err := client.GetApps(space.Id)
	var app podio.App
	if err != nil {
		fmt.Println("Failed to get apps: ", err)
		return
	}

	for _, a := range apps {
		if a.Name == appName {
			app = a
			break
		}

	}

	if app.Id == 0 {
		fmt.Println("Could not find app", appName)
		return
	}

	// Get the items
	var items podio.ItemList
	totalItems := limit
	fetchedItems := 0

	// While we're fetching some remainder of 500
	path := fmt.Sprintf("/item/app/%d/filter?fields=items.fields(files)", app.Id)
	// Do provisional request to get total item count
	err = client.RequestWithParams("POST", path, nil, map[string]interface{}{
		"limit":  1,
		"offset": 0,
	}, &items)
	if err != nil {
		fmt.Println("Error fetching items /", err)
		return
	}

	if items.Total < limit {
		totalItems = items.Total
	}

	data := make(chan *podio.Item, 10)

	go func() {
		// Loop until we have all items
		// Caveat: If items are added/deleted while looping, we get into trouble
		for fetchedItems < totalItems {
			// Re-initialize list, as pointers get mangled otherwise
			var newItems podio.ItemList
			itemsToFetch := 500

			if itemsToFetch > totalItems-fetchedItems {
				itemsToFetch = totalItems - fetchedItems
			}

			err := client.RequestWithParams("POST", path, nil, map[string]interface{}{
				"limit":  itemsToFetch,
				"offset": fetchedItems,
			}, &newItems)

			if err != nil {
				fmt.Println("Error fetching items /", err)
				return
			}

			fetchedItems += len(newItems.Items)

			for _, item := range newItems.Items {
				data <- item
				//fmt.Printf("Item: %+v\n\n", item)
			}

		}

		close(data)
	}()

	DrainCSV(";")(data, os.Stdout)
}

func DrainCSV(join string) func(<-chan *podio.Item, io.WriteCloser) {
	return func(in <-chan *podio.Item, out io.WriteCloser) {
		defer out.Close()

		data := make([]*podio.Item, 0)
		fields := make(map[string]bool)

		// Consume all data
		previewSize := 0
		for item := range in {
			data = append(data, item)

			// TODO: Collect key names
			for _, field := range item.Fields {
				fields[field.ExternalId] = true;
			}

			// Scan only the first 500 items for headers. We assume the rest to
			// be identical.
			previewSize += 1
			if previewSize > 500 {
				break
			}
		}

		// Sort the key names
		sortedKeys := make([]string, 0, len(fields))
		for k := range fields {
			sortedKeys = append(sortedKeys, k)
		}
		sort.Strings(sortedKeys)

		// Write out header
		out.Write([]byte("#"))
		// Join keys and rewrite `.` to `_` (so we don't need escpaing in all cases).
		out.Write([]byte("time"))
		out.Write([]byte(join))
		out.Write([]byte("unixtime"))
		out.Write([]byte(join))
		out.Write([]byte("name"))
		out.Write([]byte(join))
		out.Write([]byte(strings.Join(sortedKeys, join)))
		out.Write([]byte("\n"))

		// Loop over timestamps, then keys and print it all
		arr := make([]string, len(sortedKeys)+3)
		for _, item := range data {
			arr[0] = item.CreatedOn.Format(time.RFC3339Nano)
			arr[1] = fmt.Sprint(item.CreatedOn.Unix())
			arr[2] = ""

			for i, name := range sortedKeys {
				arr[i+3] = ""
				for _, field := range item.Fields {
					if field.ExternalId == name {
						arr[i+3] = FormatField(field.Values)
					}
				}
			}

			out.Write([]byte(strings.Join(arr, join)))
			out.Write([]byte("\n"))
		}

		// Then loop over the rest of the data in the cannel
		for item := range in {
			arr[0] = item.CreatedOn.Format(time.RFC3339Nano)
			arr[1] = fmt.Sprint(item.CreatedOn.Unix())
			arr[2] = ""

			for i, name := range sortedKeys {
				arr[i+3] = ""
				for _, field := range item.Fields {
					if field.ExternalId == name {
						arr[i+3] = FormatField(field.Values)
					}
				}
			}

			out.Write([]byte(strings.Join(arr, join)))
			out.Write([]byte("\n"))
		}
	}
}

// Pretty-print the given data
//
// http://godoc.org/github.com/andreas/podio-go#Item
func FormatField(field interface{}) string {
	switch value := field.(type) {
	/*
		case *podio.Value:
			return FormatField(*value)
		case podio.Value:
			return FormatField(value.Value)
	*/
	case podio.Time:
		return fmt.Sprint(value.UnixNano() / 1e6)
	case *podio.Item:
		return FormatField(*value)
	case int, *int, uint, *uint:
		return fmt.Sprint(value)

		/*
			case *float64:
				return FormatField(*value)
			case float64:
				if math.Trunc(value) == value {
					return fmt.Sprintf("%.0f", value)
				}
				return fmt.Sprintf("%f", value)
		*/

	case string, *string:
		return strings.Map(func(r rune) rune {
			switch {
			case r == ';':
				return '�'
			case r == '\n':
				return '␤'
			default:
				return r
			}
		}, fmt.Sprint(value))

	case map[string]interface{}:
		if len(value) == 1 {
			for _, v := range value {
				return FormatField(v)
			}
		}

		// Does it have .value?
		if v, ok := value["value"]; ok {
			return FormatField(v)
		}

		// Does it have .text?
		if v, ok := value["text"]; ok {
			return FormatField(v)
		}

		// Does it have .app? (= link to an application
		if v, ok := value["app"]; ok {
			return FormatField(v)
		}

		// Timestamps has .start_time_utc
		if v, ok := value["start_utc"]; ok {
			return FormatField(v)
		}

		//fmt.Println("GOT AN INTERFACE", value)
		var keys = make([]string, 0, len(value))

		for k, v := range value {
			k = strings.Replace(k, "-", "_", -1)
			k = strings.Replace(k, " ", "_", -1)
			keys = append(keys, fmt.Sprintf("%s = %s", k, FormatField(v)))
		}

		return fmt.Sprintf("{%s}", strings.Join(keys, ", "))

	case []interface{}:
		var data = make([]string, len(value))

		for i, val := range value {
			data[i] = FormatField(val)
		}

		return strings.Join(data, " / ")
	default:
		return fmt.Sprintf(`"%#v"`, value)
	}
}
