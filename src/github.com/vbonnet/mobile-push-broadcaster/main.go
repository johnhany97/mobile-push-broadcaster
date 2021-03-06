package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/alexjlockwood/gcm"
	"github.com/anachronistic/apns"
	"github.com/codegangsta/martini"
	"github.com/martini-contrib/render"
	"github.com/vbonnet/mobile-push-broadcaster/dao"
	"github.com/vbonnet/mobile-push-broadcaster/web_logs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

type AppInfo struct {
	Name           string
	AndroidDevices int
	IOSDevices     int
}

type AppSettings struct {
	Name      string `json:"name"`
	GcmApiKey string `json:"gcm_api_key"`
}

var settings struct {
	PORT string        `json:"port"`
	Apps []AppSettings `json:"apps"`
}

const MAX_GCM_TOKENS = 1000

func main() {
	dir, err := filepath.Abs(filepath.Dir(os.Args[0]))
	if err != nil {
		log.Fatal(err)
	}
	// dir := "./"

	LoadConfig(dir)

	// Load tokens from Storage
	log.Println("Load the Tokens from Storage")
	dao.LoadGCMFromStorage()
	log.Println("Tokens loaded")

	m := martini.Classic()

	m.Use(render.Renderer(render.Options{
		Directory:  dir + "/web",
		Extensions: []string{".tmpl", ".html"},
		Charset:    "UTF-8",
		Delims:     render.Delims{"{[{", "}]}"},
		IndentJSON: false,
	}))
	m.Use(martini.Static(dir + "/web"))

	m.Get("/", Index)
	m.Get("/broadcast", Broadcast)

	// GCM
	m.Post("/gcm/register", RegisterGcm)
	m.Post("/gcm/unregister", UnregisterGcm)

	// APNS
	// m.Post("/apns/register", registerGcm)
	// m.Post("/apns/unregister", unregisterGcm)
	// m.Post("/apns/register_sandbox", registerGcm)
	// m.Post("/apns/unregister_sandbox", unregisterGcm)

	// websockets to display logs in the web page
	m.Get("/sock_gcm", web_logs.SockGCM)
	m.Get("/sock_apns", web_logs.SockAPNS)

	log.Fatal(http.ListenAndServe(":"+settings.PORT, m))
	m.Run()
}

func LoadConfig(dir string) {
	configFile, err := os.Open(dir + "/config.json")
	if err != nil {
		fmt.Errorf("opening config file", err.Error())
	}

	jsonParser := json.NewDecoder(configFile)
	if err = jsonParser.Decode(&settings); err != nil {
		fmt.Errorf("parsing config file", err.Error())
	}
}

func GetAppConfig(app string) (AppSettings, error) {
	for _, element := range settings.Apps {
		if app == element.Name {
			return element, nil
		}
	}
	return AppSettings{}, errors.New("No app with the name: " + app)
}
func GetAppsInfo() []AppInfo {
	var res []AppInfo
	for _, element := range settings.Apps {
		appInfo := AppInfo{element.Name, dao.GetNbGCMTokens(element.Name), dao.GetNbAPNSTokens(element.Name)}
		res = append(res, appInfo)
	}
	return res
}

func Index(render render.Render) {
	render.HTML(200, "broadcaster", GetAppsInfo())
}

func Broadcast(render render.Render, w http.ResponseWriter, r *http.Request) {
	GCM := r.URL.Query().Get("GCM")
	APNS := r.URL.Query().Get("APNS")
	// APNSSandbox := r.URL.Query().Get("APNSSandbox")
	app := r.URL.Query().Get("app")
	title := r.URL.Query().Get("title")
	message := r.URL.Query().Get("message")

	if GCM == "true" {
		go SendGCM(app, title, message)
	}
	if APNS == "true" {
		go SendApns(title, message)
	}
	// if APNSSandbox == "true" {
	// 	go SendApnsSandbox(title, message)
	// }
}

func RegisterGcm(r *http.Request) {
	app := r.PostFormValue("app")
	token := r.PostFormValue("token")
	if token == "" || app == "" {
		log.Println("RegisterGcm: app or token empty")
		return
	}
	log.Println("Register GCM token: " + token)
	dao.AddGCMToken(app, token)
}

func UnregisterGcm(r *http.Request) {
	app := r.PostFormValue("app")
	token := r.PostFormValue("token")
	if token == "" || app == "" {
		log.Println("UnregisterGcm: app or token empty")
		return
	}
	log.Println("Unregister GCM token: " + token)
	dao.RemoveGCMToken(app, token)
}

func SendGCM(app string, title string, message string) {
	log.Println("Title: " + title)
	log.Println("Message: " + message)
	web_logs.GCMLogs("Title: " + title)
	web_logs.GCMLogs("Message: " + message)
	// Create the message to be sent.
	data := map[string]interface{}{"title": title, "message": message}

	tokens := dao.GetGCMTokens(app)

	for i := 0; i < len(tokens); i = i + MAX_GCM_TOKENS {
		max := i + MAX_GCM_TOKENS
		if max >= len(tokens) {
			max = len(tokens)
		}
		block := strconv.Itoa(i) + " - " + strconv.Itoa(max-1)
		web_logs.GCMLogs("Send block: " + block)
		log.Println("Send block: " + block)
		go SendRequestToGCM(app, data, tokens[i:max], block)
	}

	web_logs.GCMLogs("Notifications sent to " + strconv.Itoa(len(tokens)) + " Android devices")
}
func SendRequestToGCM(app string, data map[string]interface{}, toks []string, block string) {
	tokens := make([]string, len(toks))
	copy(tokens, toks)

	t1 := time.Now()
	msg := gcm.NewMessage(data, tokens...)

	appSettings, app_error := GetAppConfig(app)
	if app_error != nil {
		return
	}
	sender := &gcm.Sender{ApiKey: appSettings.GcmApiKey}

	// Send the message and receive the response after at most two retries.
	resp, err := sender.Send(msg, 2)
	if err != nil {
		log.Println("ERROR: " + err.Error())
		web_logs.GCMLogs("ERROR: " + err.Error())
	}
	if resp != nil {
		res, _ := json.Marshal(resp)
		log.Println(string(res))
		web_logs.GCMLogs("OK: " + string(res))

		if resp.Failure > 0 {
			for index, el := range resp.Results {
				if el.Error != "" && el.RegistrationID == "" {
					go dao.RemoveGCMToken(app, tokens[index])
				} else if el.RegistrationID != "" {
					go dao.RemoveGCMToken(app, tokens[index])
					go dao.AddGCMToken(app, el.RegistrationID)
				}
			}
		}
	}

	t2 := time.Now()
	var duration time.Duration = t2.Sub(t1)
	web_logs.GCMLogs("Notifications block " + block + " sent to in " + duration.String())
}

func SendApns(title string, message string) {
	for i := 0; i < 50; i++ {
		time.Sleep(50 * time.Millisecond)
		web_logs.APNSLogs("PUSH: " + strconv.Itoa(i))
	}

	payload := apns.NewPayload()
	payload.Alert = title
	payload.Badge = 42
	payload.Sound = "bingbong.aiff"

	pn := apns.NewPushNotification()
	pn.DeviceToken = "YOUR_DEVICE_TOKEN_HERE"
	pn.AddPayload(payload)

	pn.Set("title", title)
	pn.Set("message", message)

	client := apns.NewClient("gateway.push.apple.com:2195", "YOUR_CERT_PEM", "YOUR_KEY_NOENC_PEM")
	// resp := client.Send(pn)
	client.Send(pn)

	pn.PayloadString()
	// alert, _ := pn.PayloadString()
	// web_logs.APNSLogs("APNS - Alert:" + string(alert))
	// web_logs.APNSLogs("APNS - Success:" + string(resp.Success))
	// web_logs.APNSLogs("APNS - Error:" + string(resp.Error))
}

func SendApnsSandbox(title string, message string) {
	for i := 0; i < 50; i++ {
		time.Sleep(50 * time.Millisecond)
		web_logs.APNSLogs("PUSH: " + strconv.Itoa(i))
	}
	payload := apns.NewPayload()
	payload.Alert = title
	payload.Badge = 42
	payload.Sound = "bingbong.aiff"

	pn := apns.NewPushNotification()
	pn.DeviceToken = "YOUR_DEVICE_TOKEN_HERE"
	pn.AddPayload(payload)

	pn.Set("title", title)
	pn.Set("message", message)

	client := apns.NewClient("gateway.sandbox.push.apple.com:2195", "YOUR_CERT_PEM", "YOUR_KEY_NOENC_PEM")
	// resp := client.Send(pn)
	client.Send(pn)

	pn.PayloadString()
	// alert, _ := pn.PayloadString()
	// APNSLogs("APNS - Alert:" + string(alert))
	// APNSLogs("APNS - Success:" + string(resp.Success))
	// APNSLogs("APNS - Error:" + string(resp.Error))
}
