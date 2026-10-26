package tui

import (
	"context"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/nadavushka-dev/enigma/internal/daemon"
	"github.com/nadavushka-dev/enigma/internal/engine"
	"github.com/nadavushka-dev/enigma/internal/proto"
)

// hostsMsg is the ssh aliases from Backend.Hosts().
type hostsMsg struct {
	hosts []string
	err   error
}

// machineRow is one line of the Machines pane: a member or an ssh host.
type machineRow struct {
	name   string
	member bool
	self   bool
	peer   *daemon.PeerReport // nil for this machine and hosts
	mark   string
	state  string
	style  lipgloss.Style
}

// machinesPane is the Machines pane (SPEC §12.2): members with their link
// state, then ssh hosts that are not members.
type machinesPane struct {
	hosts  []string
	rows   []machineRow
	cur    int
	top    int
	height int
}

func newMachines() *machinesPane { return &machinesPane{} }

func (p *machinesPane) Title() string { return "Machines" }

func (p *machinesPane) Help() []KeyHelp {
	return []KeyHelp{{"a", "add"}, {"x", "remove"}}
}

func (p *machinesPane) Update(msg tea.Msg, s *Shared) tea.Cmd {
	switch msg := msg.(type) {
	case StatusMsg:
		first := p.rows == nil
		p.rebuild(s)
		if first {
			return fetchHosts(s.Backend)
		}
	case RefreshMsg:
		return fetchHosts(s.Backend)
	case hostsMsg:
		if msg.err == nil {
			p.hosts = msg.hosts
		}
		p.rebuild(s)
	case JumpMsg:
		for i, r := range p.rows {
			if msg.Peer != "" && r.name == msg.Peer {
				p.cur = i
			}
		}
	case tea.KeyMsg:
		return p.key(msg, s)
	}
	return nil
}

func fetchHosts(b Backend) tea.Cmd {
	return func() tea.Msg {
		h, err := b.Hosts()
		return hostsMsg{hosts: h, err: err}
	}
}

// rebuild recomputes the rows from s, keeping the cursor on the same name.
func (p *machinesPane) rebuild(s *Shared) {
	var keep string
	if p.cur < len(p.rows) {
		keep = p.rows[p.cur].name
	}
	peers := map[string]*daemon.PeerReport{}
	for i := range s.Status.Peers {
		peers[s.Status.Peers[i].Name] = &s.Status.Peers[i]
	}
	var rows []machineRow
	taken := map[string]bool{}
	for _, m := range s.Machines {
		taken[m.Name] = true
		if m.Alias != "" {
			taken[m.Alias] = true
		}
		r := machineRow{name: m.Name, member: true}
		switch pr := peers[m.Name]; {
		case m.ID == s.Status.MachineID:
			r.self, r.mark, r.state = true, "●", "this"
		case pr != nil && pr.Connected:
			r.peer, r.mark, r.state, r.style = pr, "●", "linked", StyleOK
		case pr != nil && pr.Target == "":
			r.peer, r.mark, r.state, r.style = pr, "○", "dials us", StyleDim
		default:
			r.peer, r.mark, r.state, r.style = pr, "○", "down", StyleWarn
		}
		if r.self {
			rows = append([]machineRow{r}, rows...)
		} else {
			rows = append(rows, r)
		}
	}
	for _, h := range p.hosts {
		if !taken[h] {
			rows = append(rows, machineRow{name: h, mark: "·", state: "ssh only", style: StyleDim})
		}
	}
	p.rows = rows
	p.cur = 0
	for i, r := range rows {
		if r.name == keep {
			p.cur = i
		}
	}
	p.clamp()
}

func (p *machinesPane) clamp() {
	p.cur = max(0, min(p.cur, len(p.rows)-1))
}

func (p *machinesPane) key(k tea.KeyMsg, s *Shared) tea.Cmd {
	half := max(1, p.height/2)
	switch k.String() {
	case "j", "down":
		p.cur++
	case "k", "up":
		p.cur--
	case "g":
		p.cur = 0
	case "G":
		p.cur = len(p.rows) - 1
	case "ctrl+d":
		p.cur += half
	case "ctrl+u":
		p.cur -= half
	case "a":
		return p.add(s)
	case "x":
		return p.remove(s)
	case "enter":
		return p.groups(s)
	}
	p.clamp()
	return nil
}

func (p *machinesPane) add(s *Shared) tea.Cmd {
	if p.cur >= len(p.rows) {
		return nil
	}
	r := p.rows[p.cur]
	if r.member {
		return showError(fmt.Errorf("%s is already in the fleet", r.name))
	}
	b := s.Backend
	progress := func(groups []string) tea.Cmd {
		return func() tea.Msg {
			return OpenProgress{Title: "Adding " + r.name, Run: func(ctx context.Context, out io.Writer) error {
				return b.AddMachine(ctx, r.name, groups, out)
			}}
		}
	}
	if len(s.Groups) <= 1 {
		return progress(nil)
	}
	var items []PickItem
	for _, g := range s.Groups {
		items = append(items, PickItem{Label: g.Name, Checked: g.ID == proto.MainGroup})
	}
	return func() tea.Msg {
		return OpenPick{Title: "Groups for " + r.name, Items: items, Submit: func(names []string) tea.Cmd {
			if len(names) == 0 {
				names = []string{"none"}
			}
			return progress(names)
		}}
	}
}

// groups edits which groups a member is in (SPEC §13.2).
func (p *machinesPane) groups(s *Shared) tea.Cmd {
	if p.cur >= len(p.rows) {
		return nil
	}
	r := p.rows[p.cur]
	if !r.member {
		return nil
	}
	id := ""
	for _, m := range s.Machines {
		if m.Name == r.name {
			id = m.ID
		}
	}
	var items []PickItem
	for _, g := range s.Groups {
		items = append(items, PickItem{Label: g.Name, Checked: contains(g.Members, id)})
	}
	b := s.Backend
	return func() tea.Msg {
		return OpenPick{Title: "Groups of " + r.name, Items: items, Submit: func(names []string) tea.Cmd {
			want := map[string]bool{}
			for _, n := range names {
				want[n] = true
			}
			type change struct {
				g     engine.GroupInfo
				joins bool
			}
			var changes []change
			var plan []string
			for _, g := range s.Groups {
				in := contains(g.Members, id)
				switch {
				case want[g.Name] && !in:
					changes = append(changes, change{g, true})
					plan = append(plan, r.name+" joins "+g.Name)
				case !want[g.Name] && in:
					changes = append(changes, change{g, false})
					plan = append(plan, r.name+" leaves "+g.Name)
				}
			}
			if len(changes) == 0 {
				return nil
			}
			return func() tea.Msg {
				return OpenConfirm{Question: strings.Join(plan, ", ") + ". Apply?", Yes: func() tea.Msg {
					for _, c := range changes {
						req := daemon.GroupRequest{Action: "members", ID: c.g.ID}
						if c.joins {
							req.Add = []string{id}
						} else {
							req.Remove = []string{id}
						}
						if _, err := b.GroupOp(req); err != nil {
							return ShowError{Err: err}
						}
					}
					return done(strings.Join(plan, ", "))
				}}
			}
		}}
	}
}

func (p *machinesPane) remove(s *Shared) tea.Cmd {
	if p.cur >= len(p.rows) {
		return nil
	}
	r := p.rows[p.cur]
	if !r.member {
		return nil
	}
	if r.self {
		return showError(fmt.Errorf("this is this machine: remove it from another member"))
	}
	b := s.Backend
	progress := func(uninstall bool) tea.Cmd {
		return func() tea.Msg {
			return OpenProgress{Title: "Removing " + r.name, Run: func(ctx context.Context, out io.Writer) error {
				return b.RemoveMachine(ctx, r.name, uninstall, out)
			}}
		}
	}
	return func() tea.Msg {
		return OpenConfirm{
			Question: fmt.Sprintf("Remove %s from the fleet? Its files stay.", r.name),
			Yes:      progress(false),
			AltKey:   "u", AltLabel: "remove and uninstall",
			Alt: progress(true),
		}
	}
}

func showError(err error) tea.Cmd {
	return func() tea.Msg { return ShowError{Err: err} }
}

func (p *machinesPane) View(s *Shared, width, height int, focused bool) string {
	if height <= 0 {
		return ""
	}
	// The detail wraps onto up to 3 lines under a blank one. The same number
	// of lines is reserved whichever row is selected (the most any row needs),
	// so moving the cursor never changes the list height; a short pane gives
	// it fewer lines so that at least one list row stays.
	detailH := 0
	if height >= 3 {
		most := min(3, height-2)
		for i := range p.rows {
			detailH = max(detailH, len(wrapWords(p.detailOf(i), width, most)))
		}
	}
	listH := height
	if detailH > 0 {
		listH = height - 1 - detailH
	}
	p.height = listH
	// keep the cursor visible
	if p.cur < p.top {
		p.top = p.cur
	}
	if p.cur >= p.top+listH {
		p.top = p.cur - listH + 1
	}
	p.top = max(0, min(p.top, max(0, len(p.rows)-listH)))
	lines := make([]string, 0, height)
	for i := p.top; i < p.top+listH; i++ {
		l := ""
		if i < len(p.rows) {
			r := p.rows[i]
			text := fit(fmt.Sprintf("%s %-*s  %s", r.mark, p.nameW(), r.name, r.state), width)
			switch {
			case focused && i == p.cur:
				l = StyleSelected.Render(text)
			default:
				l = r.style.Render(text)
			}
		}
		lines = append(lines, fit(l, width))
	}
	if detailH > 0 {
		lines = append(lines, fit("", width))
		detail := wrapWords(p.detail(), width, detailH)
		for i := 0; i < detailH; i++ {
			d := ""
			if i < len(detail) {
				d = detail[i]
			}
			lines = append(lines, fit(StyleDim.Render(fit(d, width)), width))
		}
	}
	return strings.Join(lines[:height], "\n")
}

// wrapWords breaks s at spaces into at most n lines of at most width cells.
// A word wider than width is cut; text beyond the last line is dropped.
func wrapWords(s string, width, n int) []string {
	if width <= 0 || n <= 0 {
		return nil
	}
	var lines []string
	cur := ""
	for _, w := range strings.Fields(s) {
		for lipgloss.Width(w) > width {
			if cur != "" {
				lines = append(lines, cur)
				cur = ""
			}
			head := strings.TrimRight(fit(w, width), " ")
			if head == "" { // a rune wider than width: take it anyway
				_, size := utf8.DecodeRuneInString(w)
				head = w[:size]
			}
			lines = append(lines, head)
			w = w[len(head):]
		}
		switch {
		case cur == "":
			cur = w
		case lipgloss.Width(cur)+1+lipgloss.Width(w) <= width:
			cur += " " + w
		default:
			lines = append(lines, cur)
			cur = w
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	if len(lines) > n {
		lines = lines[:n]
	}
	return lines
}

// detail describes the selected row.
func (p *machinesPane) detail() string { return p.detailOf(p.cur) }

// detailOf describes row i.
func (p *machinesPane) detailOf(i int) string {
	if i >= len(p.rows) {
		return ""
	}
	r := p.rows[i]
	switch {
	case !r.member:
		return "a: add to the fleet"
	case r.peer == nil:
		return ""
	}
	var parts []string
	if r.peer.Target != "" {
		parts = append(parts, r.peer.Target)
	}
	if r.peer.LastError != "" {
		parts = append(parts, "error: "+r.peer.LastError)
	}
	return strings.Join(parts, "  ")
}

// nameW is the widest name, so the states line up.
func (p *machinesPane) nameW() int {
	w := 0
	for _, r := range p.rows {
		w = max(w, lipgloss.Width(r.name))
	}
	return w
}
