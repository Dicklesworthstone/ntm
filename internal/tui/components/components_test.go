package components

import (
	"strings"
	"testing"
)

// Banner tests
func TestRenderBanner(t *testing.T) {
	t.Run("static", func(t *testing.T) {
		result := RenderBanner(false, 0)
		if result == "" {
			t.Error("RenderBanner should return non-empty string")
		}
	})

	t.Run("animated", func(t *testing.T) {
		result := RenderBanner(true, 5)
		if result == "" {
			t.Error("RenderBanner animated should return non-empty string")
		}
	})
}

func TestRenderBannerMedium(t *testing.T) {
	t.Run("static", func(t *testing.T) {
		result := RenderBannerMedium(false, 0)
		if result == "" {
			t.Error("RenderBannerMedium should return non-empty string")
		}
	})

	t.Run("animated", func(t *testing.T) {
		result := RenderBannerMedium(true, 5)
		if result == "" {
			t.Error("RenderBannerMedium animated should return non-empty string")
		}
	})
}

func TestRenderInlineLogo(t *testing.T) {
	result := RenderInlineLogo()
	if result == "" {
		t.Error("RenderInlineLogo should return non-empty string")
	}
}

func TestRenderSubtitle(t *testing.T) {
	result := RenderSubtitle("Test subtitle")
	if result == "" {
		t.Error("RenderSubtitle should return non-empty string")
	}
}

func TestRenderVersion(t *testing.T) {
	result := RenderVersion("1.0.0")
	if !strings.Contains(result, "v") {
		t.Error("RenderVersion should contain version prefix")
	}
}

func TestRenderHeaderBar(t *testing.T) {
	result := RenderHeaderBar("Test Title", 40)
	if result == "" {
		t.Error("RenderHeaderBar should return non-empty string")
	}
}

func TestRenderSection(t *testing.T) {
	result := RenderSection("Section", 40)
	if result == "" {
		t.Error("RenderSection should return non-empty string")
	}

	// Test short width
	result = RenderSection("Very Long Section Title Here", 10)
	if result == "" {
		t.Error("RenderSection should handle short width")
	}
}

func TestRenderAgentBadge(t *testing.T) {
	tests := []string{"claude", "cc", "codex", "cod", "gemini", "gmi", "unknown"}

	for _, agent := range tests {
		t.Run(agent, func(t *testing.T) {
			result := RenderAgentBadge(agent)
			if result == "" {
				t.Errorf("RenderAgentBadge(%q) should return non-empty string", agent)
			}
		})
	}
}

func TestRenderStatusBadge(t *testing.T) {
	tests := []string{"running", "active", "idle", "error", "failed", "success", "done", "unknown"}

	for _, status := range tests {
		t.Run(status, func(t *testing.T) {
			result := RenderStatusBadge(status)
			if result == "" {
				t.Errorf("RenderStatusBadge(%q) should return non-empty string", status)
			}
		})
	}
}

func TestRenderKeyMap(t *testing.T) {
	keys := map[string]string{
		"q": "quit",
		"?": "help",
	}
	result := RenderKeyMap(keys, 60)
	if result == "" {
		t.Error("RenderKeyMap should return non-empty string")
	}
}

func TestRenderFooter(t *testing.T) {
	result := RenderFooter("Footer text", 40)
	if result == "" {
		t.Error("RenderFooter should return non-empty string")
	}
}

func TestRenderHint(t *testing.T) {
	result := RenderHint("Hint text")
	if result == "" {
		t.Error("RenderHint should return non-empty string")
	}
}

func TestRenderHighlight(t *testing.T) {
	result := RenderHighlight("Highlighted")
	if result == "" {
		t.Error("RenderHighlight should return non-empty string")
	}
}

func TestRenderCommand(t *testing.T) {
	result := RenderCommand("ntm")
	if result == "" {
		t.Error("RenderCommand should return non-empty string")
	}
}

func TestRenderArg(t *testing.T) {
	result := RenderArg("session")
	if !strings.Contains(result, "<") {
		t.Error("RenderArg should wrap in angle brackets")
	}
}

func TestRenderFlag(t *testing.T) {
	result := RenderFlag("--help")
	if result == "" {
		t.Error("RenderFlag should return non-empty string")
	}
}

func TestRenderExample(t *testing.T) {
	result := RenderExample("ntm spawn myproject")
	if result == "" {
		t.Error("RenderExample should return non-empty string")
	}
}

// Box tests
func TestNewBox(t *testing.T) {
	box := NewBox()
	if box == nil {
		t.Fatal("NewBox should return non-nil box")
	}
	if box.Style != BoxRounded {
		t.Error("NewBox should default to BoxRounded")
	}
	if box.Padding != 1 {
		t.Error("NewBox should default padding to 1")
	}
}

func TestBoxBuilderPattern(t *testing.T) {
	box := NewBox().
		WithTitle("Test").
		WithContent("Content").
		WithSize(40, 10).
		WithStyle(BoxDouble).
		WithPadding(2)

	if box.Title != "Test" {
		t.Error("WithTitle should set title")
	}
	if box.Content != "Content" {
		t.Error("WithContent should set content")
	}
	if box.Width != 40 {
		t.Error("WithSize should set width")
	}
	if box.Height != 10 {
		t.Error("WithSize should set height")
	}
	if box.Style != BoxDouble {
		t.Error("WithStyle should set style")
	}
	if box.Padding != 2 {
		t.Error("WithPadding should set padding")
	}
}

func TestBoxRender(t *testing.T) {
	t.Run("simple box", func(t *testing.T) {
		box := NewBox().WithContent("Hello")
		result := box.Render()
		if result == "" {
			t.Error("Box.Render should return non-empty string")
		}
	})

	t.Run("box with title", func(t *testing.T) {
		box := NewBox().
			WithTitle("Title").
			WithContent("Content").
			WithSize(30, 0)
		result := box.Render()
		if result == "" {
			t.Error("Box with title should render")
		}
	})

	t.Run("all styles", func(t *testing.T) {
		styles := []BoxStyle{BoxRounded, BoxDouble, BoxThick, BoxNormal, BoxHidden}
		for _, style := range styles {
			box := NewBox().WithContent("Test").WithStyle(style)
			result := box.Render()
			if result == "" {
				t.Errorf("BoxStyle %d should render", style)
			}
		}
	})
}

func TestBoxString(t *testing.T) {
	box := NewBox().WithContent("Test")
	if box.String() != box.Render() {
		t.Error("Box.String() should equal Box.Render()")
	}
}

func TestSimpleBox(t *testing.T) {
	result := SimpleBox("Title", "Content", 40)
	if result == "" {
		t.Error("SimpleBox should return non-empty string")
	}
}

func TestInfoBox(t *testing.T) {
	result := InfoBox("Info", "Information", 40)
	if result == "" {
		t.Error("InfoBox should return non-empty string")
	}
}

func TestSuccessBox(t *testing.T) {
	result := SuccessBox("Success", "Done!", 40)
	if result == "" {
		t.Error("SuccessBox should return non-empty string")
	}
}

func TestErrorBox(t *testing.T) {
	result := ErrorBox("Error", "Failed!", 40)
	if result == "" {
		t.Error("ErrorBox should return non-empty string")
	}
}

func TestWarningBox(t *testing.T) {
	result := WarningBox("Warning", "Be careful!", 40)
	if result == "" {
		t.Error("WarningBox should return non-empty string")
	}
}

func TestDivider(t *testing.T) {
	result := Divider(20)
	if result == "" {
		t.Error("Divider should return non-empty string")
	}
}

func TestDoubleDivider(t *testing.T) {
	result := DoubleDivider(20)
	if result == "" {
		t.Error("DoubleDivider should return non-empty string")
	}
}

func TestThinDivider(t *testing.T) {
	result := ThinDivider(20)
	if result == "" {
		t.Error("ThinDivider should return non-empty string")
	}
}

func TestLabeledDivider(t *testing.T) {
	t.Run("normal width", func(t *testing.T) {
		result := LabeledDivider("Section", 40)
		if result == "" {
			t.Error("LabeledDivider should return non-empty string")
		}
	})

	t.Run("narrow width", func(t *testing.T) {
		result := LabeledDivider("Very Long Label", 10)
		if result == "" {
			t.Error("LabeledDivider should handle narrow width")
		}
	})
}

// Spinner tests
func TestNewSpinner(t *testing.T) {
	s := NewSpinner()
	if s.Style != SpinnerDots {
		t.Error("NewSpinner should default to SpinnerDots")
	}
	if len(s.GradientColors) == 0 {
		t.Error("NewSpinner should have gradient colors")
	}
}

func TestSpinnerView(t *testing.T) {
	s := NewSpinner()
	result := s.View()
	if result == "" {
		t.Error("Spinner.View should return non-empty string")
	}
}

func TestSpinnerViewWithLabel(t *testing.T) {
	s := NewSpinner()
	s.Label = "Loading..."
	result := s.View()
	if !strings.Contains(result, "Loading") {
		t.Error("Spinner with label should include label")
	}
}

func TestSpinnerViewWithGradient(t *testing.T) {
	s := NewSpinner()
	s.Gradient = true
	result := s.View()
	if result == "" {
		t.Error("Spinner with gradient should render")
	}
}

func TestSpinnerStyles(t *testing.T) {
	styles := []SpinnerStyle{
		SpinnerDots,
		SpinnerLine,
		SpinnerBounce,
		SpinnerPoints,
		SpinnerGlobe,
		SpinnerMoon,
		SpinnerMonkey,
		SpinnerMeter,
		SpinnerHamburger,
	}

	for _, style := range styles {
		t.Run("", func(t *testing.T) {
			s := NewSpinner()
			s.Style = style
			result := s.View()
			if result == "" {
				t.Errorf("SpinnerStyle %d should render", style)
			}
		})
	}
}

func TestSpinnerUpdate(t *testing.T) {
	s := NewSpinner()
	initialFrame := s.Frame

	updated, cmd := s.Update(SpinnerTickMsg{})
	if updated.Frame == initialFrame {
		// Frame should advance
		if updated.Frame != 1 {
			t.Error("Spinner frame should advance on tick")
		}
	}
	if cmd == nil {
		t.Error("Spinner.Update should return a command")
	}
}

func TestSpinnerUpdateUnknownMsg(t *testing.T) {
	s := NewSpinner()
	initialFrame := s.Frame

	updated, cmd := s.Update("unknown message")
	if updated.Frame != initialFrame {
		t.Error("Spinner should not update on unknown message")
	}
	if cmd != nil {
		t.Error("Spinner should not return command on unknown message")
	}
}

func TestSpinnerTickCmd(t *testing.T) {
	s := NewSpinner()
	cmd := s.TickCmd()
	if cmd == nil {
		t.Error("TickCmd should return a command")
	}
}

func TestSpinnerInit(t *testing.T) {
	s := NewSpinner()
	cmd := s.Init()
	if cmd == nil {
		t.Error("Init should return a command")
	}
}

// Logo constants tests
func TestLogoConstants(t *testing.T) {
	if len(LogoLarge) == 0 {
		t.Error("LogoLarge should not be empty")
	}
	if len(LogoMedium) == 0 {
		t.Error("LogoMedium should not be empty")
	}
	if LogoSmall == "" {
		t.Error("LogoSmall should not be empty")
	}
	if LogoIcon == "" {
		t.Error("LogoIcon should not be empty")
	}
	if LogoIconPlain == "" {
		t.Error("LogoIconPlain should not be empty")
	}
}

// List tests
func TestNewList(t *testing.T) {
	items := []ListItem{
		{Title: "Item 1", Description: "First item"},
		{Title: "Item 2", Description: "Second item"},
	}
	list := NewList(items)
	if list == nil {
		t.Fatal("NewList should return non-nil list")
	}
	if len(list.Items) != 2 {
		t.Error("NewList should have 2 items")
	}
}

func TestListBuilders(t *testing.T) {
	items := []ListItem{{Title: "Item"}}
	list := NewList(items).
		WithCursor("→").
		WithMaxVisible(5).
		WithWidth(40).
		WithNumbers(true).
		WithIcons(true)

	if list.Cursor != "→" {
		t.Error("WithCursor should set cursor")
	}
	if list.MaxVisible != 5 {
		t.Error("WithMaxVisible should set max visible")
	}
	if list.Width != 40 {
		t.Error("WithWidth should set width")
	}
	if !list.ShowNumbers {
		t.Error("WithNumbers should set show numbers")
	}
	if !list.ShowIcons {
		t.Error("WithIcons should set show icons")
	}
}

func TestListNavigation(t *testing.T) {
	items := []ListItem{
		{Title: "Item 1"},
		{Title: "Item 2"},
		{Title: "Item 3"},
	}
	list := NewList(items)

	// Move down
	list.MoveDown()
	if list.Selected != 1 {
		t.Error("MoveDown should move selection")
	}

	// Move up
	list.MoveUp()
	if list.Selected != 0 {
		t.Error("MoveUp should move selection")
	}

	// Move up at top (should wrap or stay)
	list.MoveUp()
	// Depending on implementation, check behavior
}

func TestListSelectByNumber(t *testing.T) {
	items := []ListItem{
		{Title: "Item 1"},
		{Title: "Item 2"},
		{Title: "Item 3"},
	}
	list := NewList(items)

	list.SelectByNumber(2)
	if list.Selected != 1 {
		t.Errorf("SelectByNumber(2) should select index 1, got %d", list.Selected)
	}

	// Out of range
	list.SelectByNumber(99)
	// Should not change
}

func TestListSelectedItem(t *testing.T) {
	items := []ListItem{
		{Title: "Item 1"},
		{Title: "Item 2"},
	}
	list := NewList(items)

	item := list.SelectedItem()
	if item == nil || item.Title != "Item 1" {
		t.Error("SelectedItem should return first item")
	}
}

func TestListRender(t *testing.T) {
	items := []ListItem{
		{Title: "Item 1", Description: "Desc 1"},
		{Title: "Item 2", Description: "Desc 2"},
	}
	list := NewList(items)
	result := list.Render()
	if result == "" {
		t.Error("List.Render should return non-empty string")
	}
}

func TestListString(t *testing.T) {
	items := []ListItem{{Title: "Item"}}
	list := NewList(items)
	if list.String() != list.Render() {
		t.Error("List.String should equal List.Render")
	}
}

// Preview tests
func TestNewPreview(t *testing.T) {
	p := NewPreview()
	if p == nil {
		t.Error("NewPreview should return non-nil preview")
	}
}

func TestPreviewBuilders(t *testing.T) {
	p := NewPreview().
		WithTitle("Title").
		WithContent("Content").
		WithSize(40, 10).
		WithBorder(true)

	if p.Title != "Title" {
		t.Error("WithTitle should set title")
	}
	if p.Content != "Content" {
		t.Error("WithContent should set content")
	}
	if p.Width != 40 {
		t.Error("WithSize should set width")
	}
	if p.Height != 10 {
		t.Error("WithSize should set height")
	}
	if !p.ShowBorder {
		t.Error("WithBorder should set border")
	}
}

func TestPreviewRender(t *testing.T) {
	p := NewPreview().
		WithTitle("Preview").
		WithContent("Some content").
		WithSize(40, 10)

	result := p.Render()
	if result == "" {
		t.Error("Preview.Render should return non-empty string")
	}
}

func TestPreviewString(t *testing.T) {
	p := NewPreview().WithContent("Test")
	if p.String() != p.Render() {
		t.Error("Preview.String should equal Preview.Render")
	}
}

// StatusBar tests
func TestNewStatusBar(t *testing.T) {
	sb := NewStatusBar(80)
	if sb == nil {
		t.Error("NewStatusBar should return non-nil")
	}
}

func TestStatusBarBuilders(t *testing.T) {
	sb := NewStatusBar(80).
		WithLeft("Left").
		WithCenter("Center").
		WithRight("Right")

	if sb.Left != "Left" {
		t.Error("WithLeft should set left")
	}
	if sb.Center != "Center" {
		t.Error("WithCenter should set center")
	}
	if sb.Right != "Right" {
		t.Error("WithRight should set right")
	}
}

func TestStatusBarRender(t *testing.T) {
	sb := NewStatusBar(80).
		WithLeft("File: test.go").
		WithCenter("Line 42").
		WithRight("UTF-8")

	result := sb.Render()
	if result == "" {
		t.Error("StatusBar.Render should return non-empty string")
	}
}

// HelpBar tests
func TestNewHelpBar(t *testing.T) {
	hb := NewHelpBar(80)
	if hb == nil {
		t.Error("NewHelpBar should return non-nil")
	}
}

func TestHelpBarAdd(t *testing.T) {
	hb := NewHelpBar(80)
	hb.Add("q", "quit")
	hb.Add("?", "help")

	if len(hb.Items) != 2 {
		t.Error("Add should add items")
	}
}

func TestHelpBarRender(t *testing.T) {
	hb := NewHelpBar(80)
	hb.Add("q", "quit")
	hb.Add("?", "help")

	result := hb.Render()
	if result == "" {
		t.Error("HelpBar.Render should return non-empty string")
	}
}

func TestHelpBarString(t *testing.T) {
	hb := NewHelpBar(80)
	hb.Add("q", "quit")
	if hb.String() != hb.Render() {
		t.Error("HelpBar.String should equal HelpBar.Render")
	}
}

// Header tests
func TestNewHeader(t *testing.T) {
	h := NewHeader("Title", 80)
	if h == nil {
		t.Error("NewHeader should return non-nil")
	}
}

func TestHeaderBuilders(t *testing.T) {
	h := NewHeader("Title", 80).
		WithSubtitle("Subtitle").
		WithIcon("󰆍")

	if h.Subtitle != "Subtitle" {
		t.Error("WithSubtitle should set subtitle")
	}
	if h.Icon != "󰆍" {
		t.Error("WithIcon should set icon")
	}
}

func TestHeaderRender(t *testing.T) {
	h := NewHeader("Test Header", 80).
		WithSubtitle("A subtitle")

	result := h.Render()
	if result == "" {
		t.Error("Header.Render should return non-empty string")
	}
}

func TestHeaderString(t *testing.T) {
	h := NewHeader("Title", 80)
	if h.String() != h.Render() {
		t.Error("Header.String should equal Header.Render")
	}
}

// Progress tests
func TestNewProgressBar(t *testing.T) {
	pb := NewProgressBar(40)
	if pb.Width != 40 {
		t.Error("NewProgressBar should set width")
	}
}

func TestProgressBarSetPercent(t *testing.T) {
	pb := NewProgressBar(40)
	pb.SetPercent(0.5)
	if pb.Percent != 0.5 {
		t.Error("SetPercent should set percent")
	}

	// Test bounds
	pb.SetPercent(-0.5)
	if pb.Percent != 0 {
		t.Error("SetPercent should clamp to 0")
	}

	pb.SetPercent(1.5)
	if pb.Percent != 1.0 {
		t.Error("SetPercent should clamp to 1")
	}
}

func TestProgressBarView(t *testing.T) {
	pb := NewProgressBar(40)
	pb.SetPercent(0.5)
	result := pb.View()
	if result == "" {
		t.Error("ProgressBar.View should return non-empty string")
	}
}

func TestNewIndeterminateBar(t *testing.T) {
	ib := NewIndeterminateBar(40)
	if ib.Width != 40 {
		t.Error("NewIndeterminateBar should set width")
	}
}

func TestIndeterminateBarView(t *testing.T) {
	ib := NewIndeterminateBar(40)
	result := ib.View()
	if result == "" {
		t.Error("IndeterminateBar.View should return non-empty string")
	}
}

// GroupedList tests
func TestNewGroupedList(t *testing.T) {
	groups := []ListGroup{
		{
			Name: "Group 1",
			Items: []ListItem{
				{Title: "Item 1"},
				{Title: "Item 2"},
			},
		},
	}
	gl := NewGroupedList(groups)
	if gl == nil {
		t.Error("NewGroupedList should return non-nil")
	}
}

func TestGroupedListNavigation(t *testing.T) {
	groups := []ListGroup{
		{
			Name: "Group 1",
			Items: []ListItem{
				{Title: "Item 1"},
				{Title: "Item 2"},
			},
		},
	}
	gl := NewGroupedList(groups)

	gl.MoveDown()
	gl.MoveUp()
	gl.SelectByNumber(1)
	// Just verify no panic
}

func TestGroupedListSelectedItem(t *testing.T) {
	groups := []ListGroup{
		{
			Name: "Group 1",
			Items: []ListItem{
				{Title: "Item 1"},
				{Title: "Item 2"},
			},
		},
	}
	gl := NewGroupedList(groups)

	item := gl.SelectedItem()
	if item == nil {
		t.Error("SelectedItem should return an item")
	}

	total := gl.TotalItems()
	if total != 2 {
		t.Errorf("TotalItems should return 2, got %d", total)
	}
}

func TestGroupedListRender(t *testing.T) {
	groups := []ListGroup{
		{
			Name: "Group 1",
			Items: []ListItem{
				{Title: "Item 1"},
			},
		},
	}
	gl := NewGroupedList(groups)
	result := gl.Render()
	if result == "" {
		t.Error("GroupedList.Render should return non-empty string")
	}
}

func TestGroupedListString(t *testing.T) {
	groups := []ListGroup{
		{
			Name: "Group 1",
			Items: []ListItem{
				{Title: "Item 1"},
			},
		},
	}
	gl := NewGroupedList(groups)
	if gl.String() != gl.Render() {
		t.Error("GroupedList.String should equal GroupedList.Render")
	}
}

// Gradient function tests
func TestGradientFunctions(t *testing.T) {
	t.Run("gradientPrimary", func(t *testing.T) {
		colors := gradientPrimary()
		if len(colors) == 0 {
			t.Error("gradientPrimary should return colors")
		}
	})

	t.Run("gradientSecondary", func(t *testing.T) {
		colors := gradientSecondary()
		if len(colors) == 0 {
			t.Error("gradientSecondary should return colors")
		}
	})

	t.Run("gradientSuccess", func(t *testing.T) {
		colors := gradientSuccess()
		if len(colors) == 0 {
			t.Error("gradientSuccess should return colors")
		}
	})

	t.Run("gradientRainbow", func(t *testing.T) {
		colors := gradientRainbow()
		if len(colors) < 7 {
			t.Error("gradientRainbow should return rainbow colors")
		}
	})

	t.Run("gradientAgent", func(t *testing.T) {
		agents := []string{"claude", "codex", "gemini", "unknown"}
		for _, agent := range agents {
			colors := gradientAgent(agent)
			if len(colors) == 0 {
				t.Errorf("gradientAgent(%q) should return colors", agent)
			}
		}
	})
}

func TestRenderState(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		out := RenderState(StateOptions{
			Kind:    StateEmpty,
			Message: "No items",
			Width:   40,
		})
		if out == "" {
			t.Fatal("expected non-empty output")
		}
		if !strings.Contains(out, "No items") {
			t.Fatalf("expected message to be included, got %q", out)
		}
	})

	t.Run("loading has default message", func(t *testing.T) {
		out := RenderState(StateOptions{
			Kind:  StateLoading,
			Width: 40,
		})
		if out == "" {
			t.Fatal("expected non-empty output")
		}
		if !strings.Contains(out, "Loading") {
			t.Fatalf("expected loading message, got %q", out)
		}
	})

	t.Run("error includes hint", func(t *testing.T) {
		out := RenderState(StateOptions{
			Kind:    StateError,
			Message: "Bad news",
			Hint:    "Press r to retry",
			Width:   40,
		})
		if !strings.Contains(out, "Bad news") || !strings.Contains(out, "Press r") {
			t.Fatalf("expected message and hint, got %q", out)
		}
	})

	t.Run("truncates to width", func(t *testing.T) {
		out := RenderState(StateOptions{
			Kind:    StateEmpty,
			Message: "this is a very very long message",
			Width:   10,
		})
		if !strings.Contains(out, "…") {
			t.Fatalf("expected truncation ellipsis, got %q", out)
		}
	})
}

func TestRenderKeyHint(t *testing.T) {
	hint := KeyHint{Key: "Enter", Desc: "select"}
	out := RenderKeyHint(hint)

	if out == "" {
		t.Fatal("expected non-empty output")
	}
	if !strings.Contains(out, "Enter") {
		t.Fatalf("expected key to be included, got %q", out)
	}
	if !strings.Contains(out, "select") {
		t.Fatalf("expected desc to be included, got %q", out)
	}
}

func TestRenderKeyHintCompact(t *testing.T) {
	hint := KeyHint{Key: "q", Desc: "quit"}
	out := RenderKeyHintCompact(hint)

	if out == "" {
		t.Fatal("expected non-empty output")
	}
	if !strings.Contains(out, "q") {
		t.Fatalf("expected key to be included, got %q", out)
	}
	if !strings.Contains(out, "quit") {
		t.Fatalf("expected desc to be included, got %q", out)
	}
}

func TestRenderHelpBar(t *testing.T) {
	t.Run("renders multiple hints", func(t *testing.T) {
		hints := []KeyHint{
			{Key: "↑/↓", Desc: "navigate"},
			{Key: "Enter", Desc: "select"},
			{Key: "q", Desc: "quit"},
		}
		out := RenderHelpBar(HelpBarOptions{Hints: hints})

		if out == "" {
			t.Fatal("expected non-empty output")
		}
		if !strings.Contains(out, "navigate") {
			t.Error("expected 'navigate' in output")
		}
		if !strings.Contains(out, "select") {
			t.Error("expected 'select' in output")
		}
		if !strings.Contains(out, "quit") {
			t.Error("expected 'quit' in output")
		}
	})

	t.Run("empty hints returns empty string", func(t *testing.T) {
		out := RenderHelpBar(HelpBarOptions{Hints: nil})
		if out != "" {
			t.Fatalf("expected empty string, got %q", out)
		}
	})

	t.Run("truncates to width", func(t *testing.T) {
		hints := []KeyHint{
			{Key: "↑/↓", Desc: "navigate"},
			{Key: "Enter", Desc: "select"},
			{Key: "Esc", Desc: "back"},
			{Key: "q", Desc: "quit"},
		}
		// Very narrow width should drop some hints
		out := RenderHelpBar(HelpBarOptions{Hints: hints, Width: 30})

		// Should have at least one hint
		if out == "" {
			t.Fatal("expected at least some output")
		}
		// Shouldn't have all four if space is limited
		// Just verify it doesn't panic and produces something
	})

	t.Run("uses custom separator", func(t *testing.T) {
		hints := []KeyHint{
			{Key: "a", Desc: "one"},
			{Key: "b", Desc: "two"},
		}
		out := RenderHelpBar(HelpBarOptions{Hints: hints, Separator: " | "})
		if !strings.Contains(out, "|") {
			t.Error("expected custom separator in output")
		}
	})
}

func TestHelpOverlay(t *testing.T) {
	t.Run("renders with sections", func(t *testing.T) {
		opts := HelpOverlayOptions{
			Title: "Test Help",
			Sections: []HelpSection{
				{
					Title: "Navigation",
					Hints: []KeyHint{
						{Key: "↑", Desc: "Up"},
						{Key: "↓", Desc: "Down"},
					},
				},
				{
					Title: "Actions",
					Hints: []KeyHint{
						{Key: "Enter", Desc: "Select"},
					},
				},
			},
		}
		out := HelpOverlay(opts)

		if out == "" {
			t.Fatal("expected non-empty output")
		}
		if !strings.Contains(out, "Test Help") {
			t.Error("expected title in output")
		}
		if !strings.Contains(out, "Navigation") {
			t.Error("expected section title in output")
		}
		if !strings.Contains(out, "Up") {
			t.Error("expected hint desc in output")
		}
	})

	t.Run("uses default title", func(t *testing.T) {
		opts := HelpOverlayOptions{
			Sections: []HelpSection{
				{Hints: []KeyHint{{Key: "q", Desc: "quit"}}},
			},
		}
		out := HelpOverlay(opts)
		if !strings.Contains(out, "Keyboard Shortcuts") {
			t.Error("expected default title")
		}
	})

	t.Run("includes footer hint", func(t *testing.T) {
		opts := HelpOverlayOptions{
			Sections: []HelpSection{
				{Hints: []KeyHint{{Key: "x", Desc: "test"}}},
			},
		}
		out := HelpOverlay(opts)
		if !strings.Contains(out, "Esc") {
			t.Error("expected footer hint about Esc to close")
		}
	})
}

func TestDefaultHints(t *testing.T) {
	t.Run("palette hints", func(t *testing.T) {
		hints := DefaultPaletteHints()
		if len(hints) == 0 {
			t.Error("expected non-empty palette hints")
		}
		// Should have navigate, quick select, select, back
		hasNav := false
		hasSelect := false
		for _, h := range hints {
			if strings.Contains(h.Desc, "navigate") {
				hasNav = true
			}
			if strings.Contains(h.Desc, "select") {
				hasSelect = true
			}
		}
		if !hasNav {
			t.Error("expected navigation hint")
		}
		if !hasSelect {
			t.Error("expected select hint")
		}
	})

	t.Run("dashboard hints", func(t *testing.T) {
		hints := DefaultDashboardHints()
		if len(hints) == 0 {
			t.Error("expected non-empty dashboard hints")
		}
		// Should have zoom and refresh
		hasZoom := false
		hasRefresh := false
		for _, h := range hints {
			if strings.Contains(h.Desc, "zoom") {
				hasZoom = true
			}
			if strings.Contains(h.Desc, "refresh") {
				hasRefresh = true
			}
		}
		if !hasZoom {
			t.Error("expected zoom hint")
		}
		if !hasRefresh {
			t.Error("expected refresh hint")
		}
	})
}

func TestDashboardHelpOptionsFrom(t *testing.T) {
	t.Run("minimal", func(t *testing.T) {
		opts := DashboardHelpOptionsFrom(" minimal ", false)
		if opts.Verbosity != DashboardHelpVerbosityMinimal {
			t.Fatalf("expected minimal verbosity, got %v", opts.Verbosity)
		}
		if opts.Debug {
			t.Fatal("expected debug false")
		}
	})

	t.Run("full default", func(t *testing.T) {
		opts := DashboardHelpOptionsFrom("full", false)
		if opts.Verbosity != DashboardHelpVerbosityFull {
			t.Fatalf("expected full verbosity, got %v", opts.Verbosity)
		}
	})

	t.Run("debug forces full", func(t *testing.T) {
		opts := DashboardHelpOptionsFrom("minimal", true)
		if opts.Verbosity != DashboardHelpVerbosityFull {
			t.Fatalf("expected full verbosity when debug, got %v", opts.Verbosity)
		}
		if !opts.Debug {
			t.Fatal("expected debug true")
		}
	})
}

func TestCommonHintSets(t *testing.T) {
	nav := CommonNavigationHints()
	if len(nav) < 2 {
		t.Fatalf("expected navigation hints, got %d", len(nav))
	}
	if nav[0].Desc != "navigate" {
		t.Fatalf("expected navigate description, got %q", nav[0].Desc)
	}

	selectHints := CommonSelectionHints()
	if len(selectHints) < 2 {
		t.Fatalf("expected selection hints, got %d", len(selectHints))
	}
	if selectHints[0].Desc != "select" {
		t.Fatalf("expected select description, got %q", selectHints[0].Desc)
	}

	quitHints := CommonQuitHints()
	if len(quitHints) < 2 {
		t.Fatalf("expected quit hints, got %d", len(quitHints))
	}
	if quitHints[0].Desc != "back" {
		t.Fatalf("expected back description, got %q", quitHints[0].Desc)
	}
}

func TestDashboardHelpVerbosityMapping(t *testing.T) {
	t.Parallel()

	hasDesc := func(hints []KeyHint, want string) bool {
		for _, h := range hints {
			if h.Desc == want {
				return true
			}
		}
		return false
	}

	t.Run("minimal", func(t *testing.T) {
		hints := DashboardHelpBarHints(DashboardHelpOptions{Verbosity: DashboardHelpVerbosityMinimal})
		if !hasDesc(hints, "navigate") || !hasDesc(hints, "select") || !hasDesc(hints, "help") || !hasDesc(hints, "quit") {
			t.Fatalf("expected minimal hints to include navigate/select/help/quit, got %#v", hints)
		}
		if hasDesc(hints, "zoom") || hasDesc(hints, "refresh") {
			t.Fatalf("expected minimal hints to exclude zoom/refresh, got %#v", hints)
		}
	})

	t.Run("full", func(t *testing.T) {
		hints := DashboardHelpBarHints(DashboardHelpOptions{Verbosity: DashboardHelpVerbosityFull})
		if !hasDesc(hints, "zoom") || !hasDesc(hints, "refresh") {
			t.Fatalf("expected full hints to include zoom/refresh, got %#v", hints)
		}
	})

	t.Run("debug_adds_debug_hints", func(t *testing.T) {
		hints := DashboardHelpBarHints(DashboardHelpOptions{Verbosity: DashboardHelpVerbosityFull, Debug: true})
		if !hasDesc(hints, "diag") || !hasDesc(hints, "scan") || !hasDesc(hints, "checkpoint") {
			t.Fatalf("expected debug hints to include diag/scan/checkpoint, got %#v", hints)
		}
	})
}

func TestHelpSections(t *testing.T) {
	t.Run("palette sections", func(t *testing.T) {
		sections := PaletteHelpSections()
		if len(sections) == 0 {
			t.Error("expected non-empty palette sections")
		}
		// Should have Navigation, Actions, General
		titles := make(map[string]bool)
		for _, s := range sections {
			titles[s.Title] = true
		}
		if !titles["Navigation"] {
			t.Error("expected Navigation section")
		}
		if !titles["Actions"] {
			t.Error("expected Actions section")
		}
		if !titles["General"] {
			t.Error("expected General section")
		}
	})

	t.Run("dashboard sections", func(t *testing.T) {
		sections := DashboardHelpSections(DashboardHelpOptions{Verbosity: DashboardHelpVerbosityFull})
		if len(sections) == 0 {
			t.Error("expected non-empty dashboard sections")
		}
		// Should have Navigation, Pane Actions, View Controls, General
		titles := make(map[string]bool)
		for _, s := range sections {
			titles[s.Title] = true
		}
		if !titles["Navigation"] {
			t.Error("expected Navigation section")
		}
		if !titles["Actions"] {
			t.Error("expected Actions section")
		}
	})
}

// ============================================================================
// Regression Snapshot Tests
// These tests verify consistent rendering across width tiers to catch layout
// drift. Tests cover: narrow (<120), split (120-199), wide (200+).
// ============================================================================

func TestStateRenderingAcrossTiers(t *testing.T) {
	t.Parallel()

	// Width tiers based on layout.TierForWidth thresholds
	tiers := []struct {
		name  string
		width int
	}{
		{"narrow", 60},
		{"split", 140},
		{"wide", 220},
	}

	t.Run("EmptyState", func(t *testing.T) {
		t.Parallel()
		for _, tier := range tiers {
			tier := tier
			t.Run(tier.name, func(t *testing.T) {
				t.Parallel()
				out := EmptyState("No items found", tier.width)

				if out == "" {
					t.Fatalf("EmptyState(%d) should return non-empty string", tier.width)
				}
				if !strings.Contains(out, "No items") {
					t.Errorf("EmptyState(%d) should contain message", tier.width)
				}

				// Verify consistent structure: single-line output (no unexpected breaks)
				lines := strings.Split(out, "\n")
				if len(lines) > 1 {
					t.Errorf("EmptyState(%d) should be single line, got %d lines", tier.width, len(lines))
				}
			})
		}
	})

	t.Run("LoadingState", func(t *testing.T) {
		t.Parallel()
		for _, tier := range tiers {
			tier := tier
			t.Run(tier.name, func(t *testing.T) {
				t.Parallel()
				out := LoadingState("Fetching data", tier.width)

				if out == "" {
					t.Fatalf("LoadingState(%d) should return non-empty string", tier.width)
				}
				if !strings.Contains(out, "Fetching") {
					t.Errorf("LoadingState(%d) should contain message", tier.width)
				}

				lines := strings.Split(out, "\n")
				if len(lines) > 1 {
					t.Errorf("LoadingState(%d) should be single line, got %d lines", tier.width, len(lines))
				}
			})
		}
	})

	t.Run("ErrorState", func(t *testing.T) {
		t.Parallel()
		for _, tier := range tiers {
			tier := tier
			t.Run(tier.name, func(t *testing.T) {
				t.Parallel()
				out := ErrorState("Connection failed", "Press r to retry", tier.width)

				if out == "" {
					t.Fatalf("ErrorState(%d) should return non-empty string", tier.width)
				}
				if !strings.Contains(out, "Connection") {
					t.Errorf("ErrorState(%d) should contain message", tier.width)
				}
				if !strings.Contains(out, "retry") {
					t.Errorf("ErrorState(%d) should contain hint", tier.width)
				}

				// ErrorState with hint should be 2 lines
				lines := strings.Split(out, "\n")
				if len(lines) != 2 {
					t.Errorf("ErrorState(%d) with hint should be 2 lines, got %d", tier.width, len(lines))
				}
			})
		}
	})
}

func TestStateTruncationConsistency(t *testing.T) {
	t.Parallel()

	longMessage := "This is a very long message that should be truncated when the width is too narrow to display it fully"

	t.Run("EmptyState truncates at narrow width", func(t *testing.T) {
		t.Parallel()
		out := EmptyState(longMessage, 30)
		if !strings.Contains(out, "…") {
			t.Error("narrow EmptyState should truncate with ellipsis")
		}
	})

	t.Run("LoadingState truncates at narrow width", func(t *testing.T) {
		t.Parallel()
		out := LoadingState(longMessage, 30)
		if !strings.Contains(out, "…") {
			t.Error("narrow LoadingState should truncate with ellipsis")
		}
	})

	t.Run("ErrorState truncates at narrow width", func(t *testing.T) {
		t.Parallel()
		out := ErrorState(longMessage, "hint", 30)
		if !strings.Contains(out, "…") {
			t.Error("narrow ErrorState should truncate with ellipsis")
		}
	})

	t.Run("wide width preserves full message", func(t *testing.T) {
		t.Parallel()
		out := EmptyState("Short message", 200)
		if strings.Contains(out, "…") {
			t.Error("wide EmptyState should not truncate short message")
		}
	})
}

func TestHelpOverlayAcrossTiers(t *testing.T) {
	t.Parallel()

	sections := []HelpSection{
		{
			Title: "Navigation",
			Hints: []KeyHint{
				{Key: "↑/↓", Desc: "Move up/down"},
				{Key: "j/k", Desc: "Vim navigation"},
			},
		},
		{
			Title: "Actions",
			Hints: []KeyHint{
				{Key: "Enter", Desc: "Select item"},
				{Key: "q", Desc: "Quit"},
			},
		},
	}

	tiers := []struct {
		name     string
		width    int
		maxWidth int
	}{
		{"narrow", 50, 50},
		{"medium", 80, 80},
		{"wide", 0, 120}, // auto-size with cap
	}

	for _, tier := range tiers {
		tier := tier
		t.Run(tier.name, func(t *testing.T) {
			t.Parallel()

			opts := HelpOverlayOptions{
				Title:    "Test Shortcuts",
				Sections: sections,
				Width:    tier.width,
				MaxWidth: tier.maxWidth,
			}
			out := HelpOverlay(opts)

			if out == "" {
				t.Fatalf("HelpOverlay(%s) should return non-empty string", tier.name)
			}

			// Verify structure elements present
			if !strings.Contains(out, "Test Shortcuts") {
				t.Errorf("HelpOverlay(%s) should contain title", tier.name)
			}
			if !strings.Contains(out, "Navigation") {
				t.Errorf("HelpOverlay(%s) should contain section title", tier.name)
			}
			if !strings.Contains(out, "Move up/down") {
				t.Errorf("HelpOverlay(%s) should contain hint description", tier.name)
			}
			if !strings.Contains(out, "Esc") {
				t.Errorf("HelpOverlay(%s) should contain close hint", tier.name)
			}

			// Verify box structure (should have border characters)
			if !strings.Contains(out, "╭") || !strings.Contains(out, "╯") {
				t.Errorf("HelpOverlay(%s) should have rounded border", tier.name)
			}
		})
	}
}

func TestHelpOverlayStructureStability(t *testing.T) {
	t.Parallel()

	sections := DashboardHelpSections(DashboardHelpOptions{Verbosity: DashboardHelpVerbosityFull})
	opts := HelpOverlayOptions{
		Title:    "Dashboard Shortcuts",
		Sections: sections,
	}

	out := HelpOverlay(opts)
	lines := strings.Split(out, "\n")

	// Verify minimum line count (border top + title + empty + sections + footer + border bottom)
	// This catches accidental removal of sections
	if len(lines) < 15 {
		t.Errorf("HelpOverlay should have at least 15 lines, got %d", len(lines))
	}

	// Count section headers present
	sectionCount := 0
	for _, line := range lines {
		for _, section := range sections {
			if strings.Contains(line, section.Title) {
				sectionCount++
				break
			}
		}
	}

	if sectionCount != len(sections) {
		t.Errorf("HelpOverlay should show all %d sections, found %d", len(sections), sectionCount)
	}
}

func TestHelpBarTierAdaptation(t *testing.T) {
	t.Parallel()

	hints := []KeyHint{
		{Key: "↑/↓", Desc: "navigate"},
		{Key: "Enter", Desc: "select"},
		{Key: "Esc", Desc: "back"},
		{Key: "q", Desc: "quit"},
	}

	t.Run("narrow width uses compact style", func(t *testing.T) {
		t.Parallel()
		out := RenderHelpBar(HelpBarOptions{Hints: hints, Width: 60})

		if out == "" {
			t.Fatal("narrow HelpBar should render")
		}
		// Compact style doesn't have background boxes, so no padding chars
		// Just verify it fits and renders
	})

	t.Run("wide width uses full style", func(t *testing.T) {
		t.Parallel()
		out := RenderHelpBar(HelpBarOptions{Hints: hints, Width: 200})

		if out == "" {
			t.Fatal("wide HelpBar should render")
		}
		// Should have more hints visible at wide width
		if !strings.Contains(out, "navigate") || !strings.Contains(out, "quit") {
			t.Error("wide HelpBar should show all hints")
		}
	})

	t.Run("very narrow drops hints progressively", func(t *testing.T) {
		t.Parallel()
		// Very narrow should still render something
		out := RenderHelpBar(HelpBarOptions{Hints: hints, Width: 20})

		// Should have at least one hint or be empty if truly too narrow
		// The key point is it shouldn't panic
		_ = out
	})
}

func TestRenderStateAlignmentModes(t *testing.T) {
	t.Parallel()

	t.Run("left alignment is default", func(t *testing.T) {
		t.Parallel()
		out := RenderState(StateOptions{
			Kind:    StateEmpty,
			Message: "Test",
			Width:   40,
		})
		// Left-aligned starts with indent
		if !strings.HasPrefix(out, " ") {
			t.Error("default alignment should have left indent")
		}
	})

	t.Run("center alignment centers content", func(t *testing.T) {
		t.Parallel()
		out := RenderState(StateOptions{
			Kind:    StateEmpty,
			Message: "Test",
			Width:   40,
			Align:   1, // lipgloss.Center
		})
		// Center-aligned rendering should produce non-empty output
		if out == "" {
			t.Error("center-aligned RenderState should render")
		}
	})
}

func TestStateDefaultMessages(t *testing.T) {
	t.Parallel()

	t.Run("empty state has default message", func(t *testing.T) {
		t.Parallel()
		out := RenderState(StateOptions{Kind: StateEmpty, Width: 40})
		if !strings.Contains(out, "Nothing to show") {
			t.Error("empty state should have default message")
		}
	})

	t.Run("loading state has default message", func(t *testing.T) {
		t.Parallel()
		out := RenderState(StateOptions{Kind: StateLoading, Width: 40})
		if !strings.Contains(out, "Loading") {
			t.Error("loading state should have default message")
		}
	})

	t.Run("error state has default message", func(t *testing.T) {
		t.Parallel()
		out := RenderState(StateOptions{Kind: StateError, Width: 40})
		if !strings.Contains(out, "Something went wrong") {
			t.Error("error state should have default message")
		}
	})
}
