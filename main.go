package main

import (
	"context"
	"encoding/csv"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/filepicker"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/joho/godotenv"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	// ChirpStack API imports
	"github.com/chirpstack/chirpstack/api/go/v4/api"
)

// Styles
var (
	titleStyle = lipgloss.NewStyle().
			MarginLeft(2).
			Foreground(lipgloss.Color("#FAFAFA")).
			Background(lipgloss.Color("#7D56F4")).
			Padding(0, 1)

	statusStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#FAFAFA")).
			Background(lipgloss.Color("#F25D94")).
			Padding(0, 1).
			MarginTop(1)

	helpStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#626262"))
)

// Application states
type state int

const (
	stateConnecting state = iota
	stateModeSelect       // New: Choose between Device or Gateway
	stateTenantSelect
	stateApplicationSelect
	stateDeviceProfileSelect
	stateFileSelect
	stateProcessing
	stateComplete
	stateError
)

// Run mode
type runMode int

const (
	modeDevices runMode = iota
	modeGateways
)

// List item for selections
type item struct {
	title, desc, id string
}

func (i item) FilterValue() string { return i.title }
func (i item) Title() string       { return i.title }
func (i item) Description() string { return i.desc }

// Model represents our application state
type model struct {
	state         state
	client        *grpc.ClientConn
	tenantClient  api.TenantServiceClient
	appClient     api.ApplicationServiceClient
	deviceClient  api.DeviceServiceClient
	profileClient api.DeviceProfileServiceClient
	gatewayClient api.GatewayServiceClient // New: Gateway client

	// API Token and server
	apiToken   string
	serverAddr string

	// Terminal dimensions
	width  int
	height int

	// Selection lists
	modeList    list.Model // New: Mode selection
	tenantList  list.Model
	appList     list.Model
	profileList list.Model

	// Selected items
	runMode         runMode // New: Store whether we're adding devices or gateways
	selectedTenant  string
	selectedApp     string
	selectedProfile string

	// File picker
	filepicker filepicker.Model

	// Text input for API token
	tokenInput textinput.Model

	// Status and error messages
	status string
	err    error

	// Results
	itemsCreated int // Renamed from devicesCreated
}

// Messages
type (
	connectMsg        struct{}
	tenantsLoadedMsg  []item
	appsLoadedMsg     []item
	profilesLoadedMsg []item
	itemsCreatedMsg   int // Renamed from devicesCreatedMsg
	errorMsg          error
)

func main() {
	p := tea.NewProgram(initialModel(), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		log.Fatal(err)
	}
}

func initialModel() model {
	// Initialize token input
	ti := textinput.New()
	ti.Placeholder = "Enter ChirpStack API token"
	ti.Focus()
	ti.CharLimit = 256
	ti.Width = 50
	ti.EchoMode = textinput.EchoPassword
	err := godotenv.Load()
	if err == nil {
		if token, ok := os.LookupEnv("TOKEN"); ok {
			ti.SetValue(token)
		}
	}

	// Initialize file picker
	fp := filepicker.New()
	fp.AllowedTypes = []string{".csv"}

	if cwd, err := os.Getwd(); err == nil {
		fp.CurrentDirectory = cwd
	} else {
		fp.CurrentDirectory = "."
	}

	return model{
		state:      stateConnecting,
		tokenInput: ti,
		filepicker: fp,
		serverAddr: "localhost:8081", // Default ChirpStack gRPC address
		status:     "Enter your ChirpStack API token",
		width:      80, // Default width
		height:     24, // Default height
	}
}

func (m model) Init() tea.Cmd {
	return textinput.Blink
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		listHeight := msg.Height - 8

		// Update list dimensions
		if m.modeList.Items() != nil {
			m.modeList.SetSize(msg.Width-4, listHeight)
		}
		if m.tenantList.Items() != nil {
			m.tenantList.SetSize(msg.Width-4, listHeight)
		}
		if m.appList.Items() != nil {
			m.appList.SetSize(msg.Width-4, listHeight)
		}
		if m.profileList.Items() != nil {
			m.profileList.SetSize(msg.Width-4, listHeight)
		}
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			if m.client != nil {
				m.client.Close()
			}
			return m, tea.Quit
		case "enter", " ", "ctrl+m":
			// Don't intercept Enter key when in file select state
			if m.state != stateFileSelect {
				return m.handleEnter()
			}
		}

	case connectMsg:
		return m.handleConnect()

	case tenantsLoadedMsg:
		items := make([]list.Item, len(msg))
		for i, v := range msg {
			items[i] = v
		}
		m.tenantList = list.New(items, list.NewDefaultDelegate(), m.width-4, m.height-8)
		m.tenantList.Title = "Select Tenant"
		m.state = stateTenantSelect
		return m, nil

	case appsLoadedMsg:
		items := make([]list.Item, len(msg))
		for i, v := range msg {
			items[i] = v
		}
		m.appList = list.New(items, list.NewDefaultDelegate(), m.width-4, m.height-8)
		m.appList.Title = "Select Application"
		m.state = stateApplicationSelect
		return m, nil

	case profilesLoadedMsg:
		items := make([]list.Item, len(msg))
		for i, v := range msg {
			items[i] = v
		}
		m.profileList = list.New(items, list.NewDefaultDelegate(), m.width-4, m.height-8)
		m.profileList.Title = "Select Device Profile"
		m.state = stateDeviceProfileSelect
		return m, nil

	case itemsCreatedMsg: // Renamed
		m.itemsCreated = int(msg) // Renamed
		m.state = stateComplete
		return m, nil

	case errorMsg:
		m.err = msg
		m.state = stateError
		return m, nil
	}

	// Handle state-specific updates
	switch m.state {
	case stateConnecting:
		var cmd tea.Cmd
		m.tokenInput, cmd = m.tokenInput.Update(msg)
		return m, cmd

	case stateModeSelect: // New
		var cmd tea.Cmd
		m.modeList, cmd = m.modeList.Update(msg)
		return m, cmd

	case stateTenantSelect:
		var cmd tea.Cmd
		m.tenantList, cmd = m.tenantList.Update(msg)
		return m, cmd

	case stateApplicationSelect:
		var cmd tea.Cmd
		m.appList, cmd = m.appList.Update(msg)
		return m, cmd

	case stateDeviceProfileSelect:
		var cmd tea.Cmd
		m.profileList, cmd = m.profileList.Update(msg)
		return m, cmd

	case stateFileSelect:
		var cmd tea.Cmd

		// Check for file selection first, before updating the filepicker
		if didSelect, path := m.filepicker.DidSelectFile(msg); didSelect {
			m.state = stateProcessing
			// Branch based on mode
			if m.runMode == modeDevices {
				return m, m.processCSV(path)
			} else {
				return m, m.processGatewayCSV(path) // New function
			}
		}

		m.filepicker, cmd = m.filepicker.Update(msg)
		return m, cmd
	}

	return m, nil
}

func (m model) handleEnter() (tea.Model, tea.Cmd) {
	switch m.state {
	case stateConnecting:
		if m.tokenInput.Value() != "" {
			m.apiToken = m.tokenInput.Value()
			return m, func() tea.Msg { return connectMsg{} }
		}

	case stateModeSelect: // New
		if item, ok := m.modeList.SelectedItem().(item); ok {
			if item.title == "Add Devices" {
				m.runMode = modeDevices
			} else {
				m.runMode = modeGateways
			}
			// Both modes need a tenant, so load tenants next
			return m, m.loadTenants()
		}

	case stateTenantSelect:
		if item, ok := m.tenantList.SelectedItem().(item); ok {
			m.selectedTenant = item.id
			// Branch logic: if devices, load apps. if gateways, go to file select.
			if m.runMode == modeDevices {
				return m, m.loadApplications()
			} else {
				m.state = stateFileSelect
				return m, m.filepicker.Init()
			}
		}

	case stateApplicationSelect:
		if item, ok := m.appList.SelectedItem().(item); ok {
			m.selectedApp = item.id
			return m, m.loadDeviceProfiles()
		}

	case stateDeviceProfileSelect:
		if item, ok := m.profileList.SelectedItem().(item); ok {
			m.selectedProfile = item.id
			m.state = stateFileSelect
			return m, m.filepicker.Init()
		}
	}

	return m, nil
}

func (m *model) handleConnect() (tea.Model, tea.Cmd) {
	// Connect to ChirpStack gRPC API
	conn, err := grpc.NewClient(m.serverAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return m, func() tea.Msg {
			return errorMsg(fmt.Errorf("failed to connect to ChirpStack at %s: %v", m.serverAddr, err))
		}
	}

	m.client = conn
	m.tenantClient = api.NewTenantServiceClient(conn)
	m.appClient = api.NewApplicationServiceClient(conn)
	m.deviceClient = api.NewDeviceServiceClient(conn)
	m.profileClient = api.NewDeviceProfileServiceClient(conn)
	m.gatewayClient = api.NewGatewayServiceClient(conn) // New

	// Create mode selection list
	items := []list.Item{
		item{title: "Add Devices", desc: "Bulk add devices from a CSV file"},
		item{title: "Add Gateways", desc: "Bulk add gateways from a CSV file"},
	}
	m.modeList = list.New(items, list.NewDefaultDelegate(), m.width-4, m.height-8)
	m.modeList.Title = "Select Operation"
	m.state = stateModeSelect // Go to new mode select state

	return m, nil
}

func (m model) loadTenants() tea.Cmd {
	return func() tea.Msg {
		ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+m.apiToken))

		resp, err := m.tenantClient.List(ctx, &api.ListTenantsRequest{
			Limit: 100,
		})
		if err != nil {
			return errorMsg(err)
		}

		var items []item
		for _, tenant := range resp.Result {
			items = append(items, item{
				title: tenant.Name,
				desc:  tenant.Name,
				id:    tenant.Id,
			})
		}

		return tenantsLoadedMsg(items)
	}
}

func (m model) loadApplications() tea.Cmd {
	return func() tea.Msg {
		ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+m.apiToken))

		resp, err := m.appClient.List(ctx, &api.ListApplicationsRequest{
			TenantId: m.selectedTenant,
			Limit:    100,
		})
		if err != nil {
			return errorMsg(err)
		}

		var items []item
		for _, app := range resp.Result {
			items = append(items, item{
				title: app.Name,
				desc:  app.Description,
				id:    app.Id,
			})
		}

		return appsLoadedMsg(items)
	}
}

func (m model) loadDeviceProfiles() tea.Cmd {
	return func() tea.Msg {
		ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+m.apiToken))

		resp, err := m.profileClient.List(ctx, &api.ListDeviceProfilesRequest{
			TenantId: m.selectedTenant,
			Limit:    100,
		})
		if err != nil {
			return errorMsg(err)
		}
		var items []item
		for _, profile := range resp.Result {
			items = append(items, item{
				title: profile.Name,
				desc:  profile.Name,
				id:    profile.Id,
			})
		}

		return profilesLoadedMsg(items)
	}
}

func (m model) processCSV(filepath string) tea.Cmd {
	return func() tea.Msg {
		file, err := os.Open(filepath)
		if err != nil {
			return errorMsg(err)
		}
		defer file.Close()

		reader := csv.NewReader(file)
		records, err := reader.ReadAll()
		if err != nil {
			return errorMsg(err)
		}

		ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+m.apiToken))

		created := 0
		// Skip header row if exists
		start := 0
		if len(records) > 0 && !isHexString(records[0][0]) {
			start = 1
		}

		for i := start; i < len(records); i++ {
			record := records[i]
			if len(record) < 2 {
				continue // Skip invalid records
			}

			devEui := record[0]
			name := record[1]
			description := ""
			if len(record) > 2 {
				description = record[2]
			}

			// Create device
			_, err := m.deviceClient.Create(ctx, &api.CreateDeviceRequest{
				Device: &api.Device{
					DevEui:          strings.ToLower(devEui),
					Name:            name,
					Description:     description,
					ApplicationId:   m.selectedApp,
					DeviceProfileId: m.selectedProfile,
					IsDisabled:      false,
				},
			})

			if err != nil {
				// Log error but continue with other devices
				log.Printf("Failed to create device %s: %v", devEui, err)
			} else {
				created++
				_, err := m.deviceClient.CreateKeys(ctx, &api.CreateDeviceKeysRequest{
					DeviceKeys: &api.DeviceKeys{
						DevEui: devEui,
						NwkKey: "5572404c696e6b4c6f52613230313823", // Consider making this dynamic
					},
				})
				if err != nil {
					// Log error but continue with other devices
					log.Printf("Failed to create device key %s: %v", devEui, err)
				}
			}
		}

		return itemsCreatedMsg(created) // Renamed
	}
}

// New function to process gateways
func (m model) processGatewayCSV(filepath string) tea.Cmd {
	return func() tea.Msg {
		file, err := os.Open(filepath)
		if err != nil {
			return errorMsg(err)
		}
		defer file.Close()

		reader := csv.NewReader(file)
		records, err := reader.ReadAll()
		if err != nil {
			return errorMsg(err)
		}

		ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+m.apiToken))

		created := 0
		// Skip header row if exists
		start := 0
		if len(records) > 0 && !isHexString(records[0][0]) {
			start = 1
		}

		for i := start; i < len(records); i++ {
			record := records[i]
			if len(record) < 2 {
				continue // Skip invalid records (need at least EUI and name)
			}

			gatewayEui := record[0]
			name := record[1]

			// Create gateway
			_, err := m.gatewayClient.Create(ctx, &api.CreateGatewayRequest{
				Gateway: &api.Gateway{
					GatewayId:     gatewayEui,
					Name:          name,
					TenantId:      m.selectedTenant,
					Description:   "", // As requested
					StatsInterval: 30, // As requested
				},
			})

			if err != nil {
				// Log error but continue with other gateways
				log.Printf("Failed to create gateway %s: %v", gatewayEui, err)
			} else {
				created++
			}
		}

		return itemsCreatedMsg(created) // Use renamed msg
	}
}

func isHexString(s string) bool {
	if len(s) == 0 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

func (m model) View() string {
	switch m.state {
	case stateConnecting:
		return fmt.Sprintf(
			"%s\n\n%s\n\n%s",
			titleStyle.Render("ChirpStack Bulk Provisioner"),
			m.tokenInput.View(),
			helpStyle.Render("Press Enter to connect • Press q to quit"),
		)

	case stateModeSelect: // New View
		return fmt.Sprintf(
			"%s\n\n%s\n\n%s",
			titleStyle.Render("Select Operation"),
			m.modeList.View(),
			helpStyle.Render("↑/↓: navigate • Enter: select • q: quit"),
		)

	case stateTenantSelect:
		return fmt.Sprintf(
			"%s\n\n%s\n\n%s",
			titleStyle.Render("Select Tenant"),
			m.tenantList.View(),
			helpStyle.Render("↑/↓: navigate • Enter: select • q: quit"),
		)

	case stateApplicationSelect:
		return fmt.Sprintf(
			"%s\n\n%s\n\n%s",
			titleStyle.Render("Select Application"),
			m.appList.View(),
			helpStyle.Render("↑/↓: navigate • Enter: select • q: quit"),
		)

	case stateDeviceProfileSelect:
		return fmt.Sprintf(
			"%s\n\n%s\n\n%s",
			titleStyle.Render("Select Device Profile"),
			m.profileList.View(),
			helpStyle.Render("↑/↓: navigate • Enter: select • q: quit"),
		)

	case stateFileSelect:
		title := "Select CSV File" // Default
		if m.runMode == modeDevices {
			title = "Select Device CSV File"
		} else {
			title = "Select Gateway CSV File"
		}
		return fmt.Sprintf(
			"%s\n\n%s\n\n%s",
			titleStyle.Render(title), // Dynamic title
			m.filepicker.View(),
			helpStyle.Render("Navigate and press Enter to select • Press q to quit"),
		)

	case stateProcessing:
		itemType := "items"
		if m.runMode == modeDevices {
			itemType = "devices"
		} else {
			itemType = "gateways"
		}
		return fmt.Sprintf(
			"%s\n\n%s",
			titleStyle.Render("Processing..."),
			statusStyle.Render(fmt.Sprintf("Creating %s from CSV file...", itemType)), // Dynamic msg
		)

	case stateComplete:
		itemType := "items"
		if m.runMode == modeDevices {
			itemType = "devices"
		} else {
			itemType = "gateways"
		}
		return fmt.Sprintf(
			"%s\n\n%s\n\n%s",
			titleStyle.Render("Complete!"),
			statusStyle.Render(fmt.Sprintf("Successfully created %d %s", m.itemsCreated, itemType)), // Dynamic msg
			helpStyle.Render("Press q to quit"),
		)

	case stateError:
		return fmt.Sprintf(
			"%s\n\n%s\n\n%s",
			titleStyle.Render("Error"),
			statusStyle.Render(fmt.Sprintf("Error: %v", m.err)),
			helpStyle.Render("Press q to quit"),
		)
	}

	return ""
}
