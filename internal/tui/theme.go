package tui

import "github.com/charmbracelet/lipgloss"

// theme is a named color palette. Every renderable style in the TUI is rebuilt
// from one of these so the whole workspace can be re-skinned at runtime, the way
// the footer companion can be swapped. Colors are 256-color terminal codes.
type theme struct {
	Name      string
	Primary   string // banners, headers, accents, fills
	Secondary string // pane titles, @refs, commands
	Accent    string // pet + live-stream text
	Danger    string // deletions, over-budget
	Dim       string // secondary prose
	Muted     string // tertiary prose
	Faint     string // empty meter cells
	Border    string // app frame + scroll track
	Bright    string // strong prose on dark bg
	OnPrimary string // text painted on a Primary background (badges)
}

// themes are cycled by /theme and the [f3] key. The first is the default and
// matches apex-code's original emerald palette.
var themes = []theme{
	{Name: "emerald", Primary: "10", Secondary: "12", Accent: "11", Danger: "9", Dim: "244", Muted: "241", Faint: "240", Border: "238", Bright: "252", OnPrimary: "0"},
	{Name: "ocean", Primary: "39", Secondary: "44", Accent: "45", Danger: "203", Dim: "244", Muted: "240", Faint: "238", Border: "24", Bright: "252", OnPrimary: "0"},
	{Name: "sunset", Primary: "208", Secondary: "205", Accent: "220", Danger: "196", Dim: "245", Muted: "241", Faint: "238", Border: "94", Bright: "230", OnPrimary: "0"},
	{Name: "grape", Primary: "141", Secondary: "213", Accent: "219", Danger: "203", Dim: "245", Muted: "242", Faint: "238", Border: "54", Bright: "252", OnPrimary: "0"},
	{Name: "mono", Primary: "252", Secondary: "248", Accent: "250", Danger: "244", Dim: "245", Muted: "241", Faint: "238", Border: "240", Bright: "255", OnPrimary: "0"},
}

// Style variables rebuilt by applyTheme. They are read at render time, so a
// theme switch takes effect on the next frame across the whole UI.
var (
	styleAppFrame      lipgloss.Style
	styleHeader        lipgloss.Style
	styleBanner        lipgloss.Style
	styleDim           lipgloss.Style
	styleMuted         lipgloss.Style
	styleBadgeOn       lipgloss.Style
	styleBadgeOff      lipgloss.Style
	stylePaneTitle     lipgloss.Style
	styleAdd           lipgloss.Style
	styleDel           lipgloss.Style
	styleSuggestionSel lipgloss.Style
	styleSuggestion    lipgloss.Style
	styleEntrySelected lipgloss.Style
	styleMeterFill     lipgloss.Style
	styleMeterEmpty    lipgloss.Style
	styleMeterOver     lipgloss.Style
	styleHelpHeader    lipgloss.Style
	styleHelpCmd       lipgloss.Style
	styleHelpUsage     lipgloss.Style
	styleRef           lipgloss.Style
	stylePet           lipgloss.Style

	styleScrollThumb lipgloss.Style
	styleScrollTrack lipgloss.Style

	styleStreamScramble lipgloss.Style
	styleStreamLive     lipgloss.Style
)

func init() { applyTheme(0) }

// themeName returns the display name for a theme index (wrapping defensively).
func themeName(i int) string {
	if len(themes) == 0 {
		return "default"
	}
	return themes[((i%len(themes))+len(themes))%len(themes)].Name
}

// applyTheme rebuilds every style var from the theme at index i (wrapping).
func applyTheme(i int) {
	if len(themes) == 0 {
		return
	}
	t := themes[((i%len(themes))+len(themes))%len(themes)]
	c := func(code string) lipgloss.Color { return lipgloss.Color(code) }

	styleAppFrame = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(c(t.Border)).Padding(1, 2)
	styleHeader = lipgloss.NewStyle().Bold(true).Foreground(c(t.Primary))
	styleBanner = lipgloss.NewStyle().Bold(true).Foreground(c(t.Primary))
	styleDim = lipgloss.NewStyle().Foreground(c(t.Dim))
	styleMuted = lipgloss.NewStyle().Foreground(c(t.Muted))
	styleBadgeOn = lipgloss.NewStyle().Foreground(c(t.OnPrimary)).Background(c(t.Primary)).Padding(0, 1)
	styleBadgeOff = lipgloss.NewStyle().Foreground(c(t.Bright)).Background(c(t.Border)).Padding(0, 1)
	stylePaneTitle = lipgloss.NewStyle().Bold(true).Foreground(c(t.Secondary))
	styleAdd = lipgloss.NewStyle().Foreground(c(t.Primary))
	styleDel = lipgloss.NewStyle().Foreground(c(t.Danger))
	styleSuggestionSel = lipgloss.NewStyle().Foreground(c(t.OnPrimary)).Background(c(t.Primary)).Padding(0, 1)
	styleSuggestion = lipgloss.NewStyle().Foreground(c(t.Bright)).Padding(0, 1)
	styleEntrySelected = lipgloss.NewStyle().BorderLeft(true).BorderForeground(c(t.Primary)).PaddingLeft(1)
	styleMeterFill = lipgloss.NewStyle().Foreground(c(t.Primary))
	styleMeterEmpty = lipgloss.NewStyle().Foreground(c(t.Faint))
	styleMeterOver = lipgloss.NewStyle().Foreground(c(t.Danger))
	styleHelpHeader = lipgloss.NewStyle().Bold(true).Foreground(c(t.Primary))
	styleHelpCmd = lipgloss.NewStyle().Bold(true).Foreground(c(t.Secondary))
	styleHelpUsage = lipgloss.NewStyle().Foreground(c(t.Bright))
	styleRef = lipgloss.NewStyle().Bold(true).Foreground(c(t.Secondary))
	stylePet = lipgloss.NewStyle().Foreground(c(t.Accent))

	styleScrollThumb = lipgloss.NewStyle().Foreground(c(t.Primary))
	styleScrollTrack = lipgloss.NewStyle().Foreground(c(t.Border))

	styleStreamScramble = lipgloss.NewStyle().Bold(true).Foreground(c(t.Primary))
	styleStreamLive = lipgloss.NewStyle().Foreground(c(t.Accent))
}
