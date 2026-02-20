package handler

import (
	"encoding/json"
	"os"

	"github.com/appwrite/sdk-for-go/appwrite"
	"github.com/open-runtimes/types-for-go/v4/openruntimes"
)

// Define the structure of the data coming from Flutter
type Payload struct {
	Type    string `json:"type"`    // "order_update" or "broadcast"
	UserId  string `json:"userId"`  // Used if type is "order_update"
	Topic   string `json:"topic"`   // Used if type is "broadcast" (e.g., "all_users")
	Title   string `json:"title"`
	Message string `json:"message"`
	OrderId string `json:"orderId"` // Optional, for order tracking
}

func Main(Context openruntimes.Context) openruntimes.Response {
	// 1. Initialize Appwrite Client
	client := appwrite.NewClient(
		appwrite.WithEndpoint("https://cloud.appwrite.io/v1"),
		appwrite.WithProject(os.Getenv("APPWRITE_FUNCTION_PROJECT_ID")),
		appwrite.WithKey(os.Getenv("APPWRITE_API_KEY")), // Needs 'messages.write' scope
	)

	messaging := appwrite.NewMessaging(client)

	// 2. Parse the Incoming JSON Payload
	if Context.Req.BodyRaw() == "" {
		return Context.Res.Json(map[string]interface{}{
			"status":  "error",
			"message": "Request body is empty",
		})
	}

	var payload Payload
	err := json.Unmarshal([]byte(Context.Req.BodyRaw()), &payload)
	if err != nil {
		Context.Error("Failed to parse JSON: " + err.Error())
		return Context.Res.Json(map[string]interface{}{
			"status":  "error",
			"message": "Invalid JSON format",
		})
	}

	// 3. Prepare Notification Data
	messageId := "unique()" // Appwrite will generate a unique ID
	data := map[string]interface{}{
		"click_action": "FLUTTER_NOTIFICATION_CLICK",
		"order_id":     payload.OrderId,
		"type":         payload.Type,
	}

	// 4. Route the Notification based on the 'Type'
	if payload.Type == "broadcast" {
		// Store is sending a message to a topic (e.g., all users or specific area)
		_, err := messaging.CreatePush(
			messageId,
			messaging.WithCreatePushTitle(payload.Title),
			messaging.WithCreatePushBody(payload.Message),
			messaging.WithCreatePushTopics([]string{payload.Topic}),
			messaging.WithCreatePushData(data),
		)

		if err != nil {
			Context.Error("Broadcast failed: " + err.Error())
			return Context.Res.Json(map[string]interface{}{"status": "error", "message": err.Error()})
		}
		
		Context.Log("✅ Broadcast sent to topic: " + payload.Topic)

	} else {
		// System is sending a specific order update to a single user
		_, err := messaging.CreatePush(
			messageId,
			messaging.WithCreatePushTitle(payload.Title),
			messaging.WithCreatePushBody(payload.Message),
			messaging.WithCreatePushUsers([]string{payload.UserId}),
			messaging.WithCreatePushData(data),
		)

		if err != nil {
			Context.Error("Order update failed: " + err.Error())
			return Context.Res.Json(map[string]interface{}{"status": "error", "message": err.Error()})
		}
		
		Context.Log("✅ Order update sent to user: " + payload.UserId)
	}

	return Context.Res.Json(map[string]interface{}{
		"status":  "success",
		"message": "Notification processed successfully",
	})
}

// package handler

// import (
// 	"os"
// 	"strconv"

// 	"github.com/appwrite/sdk-for-go/appwrite"
// 	"github.com/open-runtimes/types-for-go/v4/openruntimes"
// )

// type Response struct {
// 	Motto       string `json:"motto"`
// 	Learn       string `json:"learn"`
// 	Connect     string `json:"connect"`
// 	GetInspired string `json:"getInspired"`
// }

// // This Appwrite function will be executed every time your function is triggered
// func Main(Context openruntimes.Context) openruntimes.Response {
// 	// You can use the Appwrite SDK to interact with other services
// 	// For this example, we're using the Users service
// 	client := appwrite.NewClient(
// 		appwrite.WithEndpoint(os.Getenv("APPWRITE_FUNCTION_API_ENDPOINT")),
// 		appwrite.WithProject(os.Getenv("APPWRITE_FUNCTION_PROJECT_ID")),
// 		appwrite.WithKey(Context.Req.Headers["x-appwrite-key"]),
// 	)
// 	users := appwrite.NewUsers(client)

// 	response, err := users.List()
// 	if err != nil {
// 		Context.Error("Could not list users: " + err.Error())
// 	} else {
// 		// Log messages and errors to the Appwrite Console
// 		// These logs won't be seen by your end users
// 		Context.Log("Total users: " + strconv.Itoa(response.Total))
// 	}

// 	// The req object contains the request data
// 	if Context.Req.Path == "/ping" {
// 		// Use res object to respond with text(), json(), or binary()
// 		// Don't forget to return a response!
// 		return Context.Res.Text("Pong")
// 	}

// 	return Context.Res.Json(Response{
// 		Motto:       "Build like a team of hundreds_",
// 		Learn:       "https://appwrite.io/docs",
// 		Connect:     "https://appwrite.io/discord",
// 		GetInspired: "https://builtwith.appwrite.io",
// 	})
// }
