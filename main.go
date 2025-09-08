package main

import (
	"context"
	"encoding/csv"
	"fmt"
	"log"
	"os"

	"github.com/charmbracelet/bubbles/filepicker"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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
	stateTenantSelect
	stateApplicationSelect
	stateDeviceProfileSelect
	stateFileSelect
	stateProcessing
	stateComplete
	stateError
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

	// API Token and server
	apiToken   string
	serverAddr string

	// Terminal dimensions
	width  int
	height int

	// Selection lists
	tenantList  list.Model
	appList     list.Model
	profileList list.Model

	// Selected items
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
	devicesCreated int
}

// Messages
type (
	connectMsg        struct{}
	tenantsLoadedMsg  []item
	appsLoadedMsg     []item
	profilesLoadedMsg []item
	devicesCreatedMsg int
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

	// Initialize file picker
	fp := filepicker.New()
	fp.AllowedTypes = []string{".csv"}
	fp.CurrentDirectory, _ = os.UserHomeDir()

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

		// Update list dimensions
		if m.tenantList.Items() != nil {
			m.tenantList.SetSize(msg.Width-4, msg.Height-8)
		}
		if m.appList.Items() != nil {
			m.appList.SetSize(msg.Width-4, msg.Height-8)
		}
		if m.profileList.Items() != nil {
			m.profileList.SetSize(msg.Width-4, msg.Height-8)
		}
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			if m.client != nil {
				m.client.Close()
			}
			return m, tea.Quit
		case "enter":
			return m.handleEnter()
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

	case devicesCreatedMsg:
		m.devicesCreated = int(msg)
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
		m.filepicker, cmd = m.filepicker.Update(msg)
		if didSelect, path := m.filepicker.DidSelectFile(msg); didSelect {
			return m, m.processCSV(path)
		}
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

	case stateTenantSelect:
		if item, ok := m.tenantList.SelectedItem().(item); ok {
			m.selectedTenant = item.id
			return m, m.loadApplications()
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

func (m model) handleConnect() (tea.Model, tea.Cmd) {
	// Connect to ChirpStack gRPC API using insecure connection (as per docker-compose config)
	conn, err := grpc.Dial(m.serverAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return m, func() tea.Msg {
			return errorMsg(fmt.Errorf("failed to connect to ChirpStack at %s: %v\nMake sure ChirpStack gRPC API is running on this address", m.serverAddr, err))
		}
	}

	m.client = conn
	m.tenantClient = api.NewTenantServiceClient(conn)
	m.appClient = api.NewApplicationServiceClient(conn)
	m.deviceClient = api.NewDeviceServiceClient(conn)
	m.profileClient = api.NewDeviceProfileServiceClient(conn)

	return m, m.loadTenants()
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
					DevEui:          devEui,
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
			}
		}

		return devicesCreatedMsg(created)
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
			titleStyle.Render("ChirpStack Device Manager"),
			m.tokenInput.View(),
			helpStyle.Render("Press Enter to connect • Press q to quit"),
		)

	case stateTenantSelect:
		return fmt.Sprintf(
			"%s\n\n%s\n\n%s",
			titleStyle.Render("ChirpStack Device Manager"),
			m.tenantList.View(),
			helpStyle.Render("↑/↓: navigate • Enter: select • q: quit"),
		)

	case stateApplicationSelect:
		return fmt.Sprintf(
			"%s\n\n%s\n\n%s",
			titleStyle.Render("ChirpStack Device Manager"),
			m.appList.View(),
			helpStyle.Render("↑/↓: navigate • Enter: select • q: quit"),
		)

	case stateDeviceProfileSelect:
		return fmt.Sprintf(
			"%s\n\n%s\n\n%s",
			titleStyle.Render("ChirpStack Device Manager"),
			m.profileList.View(),
			helpStyle.Render("↑/↓: navigate • Enter: select • q: quit"),
		)

	case stateFileSelect:
		return fmt.Sprintf(
			"%s\n\n%s\n\n%s",
			titleStyle.Render("Select CSV File"),
			m.filepicker.View(),
			helpStyle.Render("Navigate and press Enter to select • Press q to quit"),
		)

	case stateProcessing:
		return fmt.Sprintf(
			"%s\n\n%s",
			titleStyle.Render("Processing..."),
			statusStyle.Render("Creating devices from CSV file..."),
		)

	case stateComplete:
		return fmt.Sprintf(
			"%s\n\n%s\n\n%s",
			titleStyle.Render("Complete!"),
			statusStyle.Render(fmt.Sprintf("Successfully created %d devices", m.devicesCreated)),
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
