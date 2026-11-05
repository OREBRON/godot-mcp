package project

import (
	"bufio"
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// FindLimit caps the hits one answer holds, so that a large project cannot
// flood an agent's context.
const FindLimit = 200

// Ref is one line of a project file that uses a path.
type Ref struct {
	File string `json:"file"`
	Line int    `json:"line"`
	Kind string `json:"kind"` // ext_resource, autoload, setting, preload, load, uid, class or string
	Text string `json:"text"`
}

// Refs is what FindRefs answers.
type Refs struct {
	Path       string   `json:"path"`
	UID        string   `json:"uid,omitempty"`
	ClassName  string   `json:"class_name,omitempty"`
	References []Ref    `json:"references"`
	Truncated  bool     `json:"truncated"`
	Notes      []string `json:"notes,omitempty"`
}

// NodeHit is a scene node of the type FindNodes looks for.
type NodeHit struct {
	Scene    string `json:"scene"`
	Node     string `json:"node"`
	Line     int    `json:"line"`
	Instance string `json:"instance,omitempty"` // the scene the node instances
}

// Nodes is what FindNodes answers.
type Nodes struct {
	NodeType  string    `json:"node_type"`
	Scripts   []string  `json:"scripts,omitempty"` // scripts declaring class_name NodeType
	Nodes     []NodeHit `json:"nodes"`
	Truncated bool      `json:"truncated"`
	Notes     []string  `json:"notes,omitempty"`
}

// MissingError is a path or uid that names no file, with existing names
// close to it.
type MissingError struct {
	Target  string
	Similar []string
}

func (e *MissingError) Error() string { return e.Target + " does not exist" }

// files are the project's files as Godot's file system sees them.
type files struct {
	text   []string          // res:// paths of files that can hold references
	known  map[string]bool   // the same, for checking paths read from files
	all    []string          // every resource file, for suggestions
	uids   map[string]string // uid:// -> res://
	binary int               // .scn and .res files, which cannot be scanned
}

var textExts = []string{".tscn", ".tres", ".gd", ".cs", ".gdshader", ".gdshaderinc", ".gdextension"}

// walk lists the project's files, skipping the data folder, hidden folders
// and folders holding .gdignore, as Godot does.
func (p *Project) walk() (*files, error) {
	f := &files{uids: map[string]string{}, known: map[string]bool{}}
	err := filepath.WalkDir(p.Dir, func(osPath string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable folders are skipped, not fatal
		}
		if d.IsDir() && osPath != p.Dir && (osPath == p.DataDir() || strings.HasPrefix(d.Name(), ".") || exists(filepath.Join(osPath, ".gdignore"))) {
			return filepath.SkipDir
		}
		// A link may lead out of the project; Godot's own scan skips it too.
		if d.Type().IsRegular() {
			f.add(p, osPath)
		}
		return nil
	})
	return f, err
}

// add files one file by its extension and records the uids it declares.
func (f *files) add(p *Project, osPath string) {
	rel, err := filepath.Rel(p.Dir, osPath)
	if err != nil {
		return
	}
	res := "res://" + filepath.ToSlash(rel)
	switch ext := strings.ToLower(path.Ext(res)); {
	case ext == ".uid":
		b, _ := os.ReadFile(osPath)
		f.addUID(strings.TrimSpace(string(b)), strings.TrimSuffix(res, path.Ext(res)))
		return
	case ext == ".import":
		f.addUID(importUID(osPath), strings.TrimSuffix(res, path.Ext(res)))
		return
	case ext == ".scn" || ext == ".res":
		f.binary++
	case res == "res://project.godot" || slices.Contains(textExts, ext):
		f.text = append(f.text, res)
		f.known[res] = true
		if ext == ".tscn" || ext == ".tres" {
			f.addUID(headerUID(osPath), res)
		}
	}
	f.all = append(f.all, res)
}

func (f *files) addUID(uid, res string) {
	if strings.HasPrefix(uid, "uid://") {
		f.uids[uid] = res
	}
}

func (f *files) uidOf(res string) string {
	for uid, r := range f.uids {
		if r == res {
			return uid
		}
	}
	return ""
}

func (f *files) notes() []string {
	if f.binary == 0 {
		return nil
	}
	return []string{fmt.Sprintf("%d binary .scn/.res files were not scanned; save them as .tscn/.tres to include them", f.binary)}
}

// importUID is the uid of an imported asset, from its .import file.
func importUID(osPath string) string {
	uid := ""
	scanCfg(osPath, func(section, key, val string) {
		if section == "remap" && key == "uid" {
			uid = val
		}
	})
	return uid
}

// headerUID is the uid in a text resource's first line, such as
// [gd_scene format=3 uid="uid://..."].
func headerUID(osPath string) string {
	file, err := os.Open(osPath)
	if err != nil {
		return ""
	}
	defer file.Close()
	line, _ := bufio.NewReader(file).ReadString('\n')
	return attr(line, "uid")
}

// attr is the quoted value of key="..." in a resource header line.
func attr(line, key string) string {
	_, rest, ok := strings.Cut(line, " "+key+`="`)
	if !ok {
		return ""
	}
	v, _, _ := strings.Cut(rest, `"`)
	return v
}

func exists(osPath string) bool {
	_, err := os.Stat(osPath)
	return err == nil
}

// osPath is a res:// path from walk as an OS path; such paths are already
// inside the project, so ToOS's checks are not needed.
func (p *Project) osPath(res string) string {
	return filepath.Join(p.Dir, filepath.FromSlash(strings.TrimPrefix(res, "res://")))
}

func capped[T any](s []T) ([]T, bool) {
	if len(s) > FindLimit {
		return s[:FindLimit], true
	}
	return s, false
}

// FindRefs lists every line of the project's text files that uses target,
// a res:// path, project-relative path or uid://: as an ext_resource, a
// project setting or autoload, a load()/preload() or other string, or, for
// a script with class_name, the class name in other scripts.
func (p *Project) FindRefs(target string) (*Refs, error) {
	f, err := p.walk()
	if err != nil {
		return nil, err
	}
	res, err := f.resolve(p, target)
	if err != nil {
		return nil, err
	}
	out := &Refs{Path: res, UID: f.uidOf(res), References: []Ref{}, Notes: f.notes()}
	if hasExt(res, ".gd") {
		b, _ := os.ReadFile(p.osPath(res))
		out.ClassName = className(b)
	}
	needles := []needle{{text: res}}
	if out.UID != "" {
		needles = append(needles, needle{text: out.UID, byUID: true})
	}
	for _, file := range f.text {
		if file == res {
			continue
		}
		if b, err := os.ReadFile(p.osPath(file)); err == nil {
			out.References = append(out.References, scanRefs(file, b, append(needles, relative(file, res)...), out.ClassName)...)
		}
		if len(out.References) > FindLimit {
			break
		}
	}
	out.References, out.Truncated = capped(out.References)
	if len(out.References) == 0 {
		out.Notes = append(out.Notes, "no file names it, but a path built at runtime (joined strings, paths read from data) is not found; check before deleting")
	}
	return out, nil
}

// resolve turns target into the res:// path of an existing file.
func (f *files) resolve(p *Project, target string) (string, error) {
	if strings.HasPrefix(target, "uid://") {
		if res, ok := f.uids[target]; ok {
			return res, nil
		}
		uids := make([]string, 0, len(f.uids))
		for uid := range f.uids {
			uids = append(uids, uid)
		}
		return "", &MissingError{Target: target, Similar: similar(target, uids)}
	}
	res, err := p.ToRes(target)
	if err != nil {
		return "", err
	}
	fi, err := os.Stat(p.osPath(res))
	if errors.Is(err, os.ErrNotExist) {
		return "", &MissingError{Target: res, Similar: similar(res, f.all)}
	}
	if err == nil && fi.IsDir() {
		err = fmt.Errorf("%s is a folder; pass a file", res)
	}
	if err != nil {
		return "", err
	}
	return res, p.CheckCase(res)
}

// similar lists up to five of have whose base name is close to want's,
// closest first, with the measure of Godot's String.similarity.
func similar(want string, have []string) []string {
	w := strings.ToLower(path.Base(want))
	score := map[string]float64{}
	var close []string
	for _, h := range have {
		if v := similarity(w, strings.ToLower(path.Base(h))); v > 0.6 {
			score[h] = v
			close = append(close, h)
		}
	}
	sort.SliceStable(close, func(i, j int) bool { return score[close[i]] > score[close[j]] })
	return close[:min(len(close), 5)]
}

// similarity is the Sørensen-Dice coefficient of two strings' letter pairs.
func similarity(a, b string) float64 {
	if a == b {
		return 1
	}
	pairs := map[string]int{}
	for i := 0; i+1 < len(a); i++ {
		pairs[a[i:i+2]]++
	}
	common := 0
	for i := 0; i+1 < len(b); i++ {
		if pairs[b[i:i+2]] > 0 {
			pairs[b[i:i+2]]--
			common++
		}
	}
	if len(a)+len(b) <= 2 {
		return 0
	}
	return 2 * float64(common) / float64(len(a)+len(b)-2)
}

// needle is a way a file can name the path FindRefs looks for.
type needle struct {
	text     string
	byUID    bool
	loadOnly bool // a relative path counts only inside load() or preload()
}

// relative is res as a script in file may name it, relative to the
// script's folder: "player.tscn", "./player.tscn" or "../x/player.tscn".
func relative(file, res string) []needle {
	if !hasExt(file, ".gd") {
		return nil
	}
	dir := path.Dir(strings.TrimPrefix(file, "res://"))
	rel, err := filepath.Rel(filepath.FromSlash(dir), filepath.FromSlash(strings.TrimPrefix(res, "res://")))
	if err != nil {
		return nil
	}
	rel = filepath.ToSlash(rel)
	out := []needle{{text: rel, loadOnly: true}}
	if !strings.HasPrefix(rel, "../") {
		out = append(out, needle{text: "./" + rel, loadOnly: true})
	}
	return out
}

// scanRefs lists the lines of file that hold one of needles as a whole
// string, or, in a script, class as an identifier.
func scanRefs(file string, data []byte, needles []needle, class string) []Ref {
	var refs []Ref
	section := ""
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = line[1 : len(line)-1]
		}
		kind := ""
		if n := slices.IndexFunc(needles, func(nd needle) bool { return names(line, nd) }); n >= 0 {
			kind = refKind(file, section, line, needles[n].byUID)
		} else if class != "" && hasExt(file, ".gd") && !strings.HasPrefix(line, "#") && occurs(line, class, wordAt(line)) {
			kind = "class"
		}
		if kind != "" {
			refs = append(refs, Ref{File: file, Line: i + 1, Kind: kind, Text: clip(line)})
		}
	}
	return refs
}

func names(line string, nd needle) bool {
	return (!nd.loadOnly || strings.Contains(line, "load(")) && occurs(line, nd.text, quotedAt(line))
}

// refKind says how a line that holds a path uses it.
func refKind(file, section, line string, byUID bool) string {
	switch {
	case strings.HasPrefix(line, "[ext_resource"):
		return "ext_resource"
	case file == "res://project.godot" && section == "autoload":
		return "autoload"
	case file == "res://project.godot":
		return "setting"
	case strings.Contains(line, "preload("):
		return "preload"
	case strings.Contains(line, "load("):
		return "load"
	case byUID:
		return "uid"
	}
	return "string"
}

// occurs reports whether needle occurs in line at a place ok accepts.
func occurs(line, needle string, ok func(start, end int) bool) bool {
	for i := 0; ; {
		j := strings.Index(line[i:], needle)
		if j < 0 {
			return false
		}
		if ok(i+j, i+j+len(needle)) {
			return true
		}
		i += j + len(needle)
	}
}

// quotedAt accepts a whole string: after a quote (or an autoload's "*")
// and before a closing quote.
func quotedAt(line string) func(start, end int) bool {
	return func(start, end int) bool {
		return start > 0 && strings.ContainsRune(`"'*`, rune(line[start-1])) && end < len(line) && strings.ContainsRune(`"'`, rune(line[end]))
	}
}

// wordAt accepts a whole identifier.
func wordAt(line string) func(start, end int) bool {
	return func(start, end int) bool { return !identAt(line, start-1) && !identAt(line, end) }
}

func identAt(s string, i int) bool {
	if i < 0 || i >= len(s) {
		return false
	}
	c := s[i]
	return c == '_' || '0' <= c && c <= '9' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z'
}

func clip(line string) string {
	if len(line) > 160 {
		return line[:160] + "..."
	}
	return line
}

func hasExt(res, ext string) bool { return strings.EqualFold(path.Ext(res), ext) }

// className is the class_name a script declares, or "".
func className(data []byte) string {
	for _, line := range strings.Split(string(data), "\n") {
		if f := strings.Fields(line); len(f) >= 2 && f[0] == "class_name" {
			return f[1]
		}
	}
	return ""
}

// FindNodes lists the scene nodes whose type is class, or whose script
// declares class_name class. A node that instances a scene counts by that
// scene's root; the nodes inside it are listed under their own scene.
func (p *Project) FindNodes(class string) (*Nodes, error) {
	f, err := p.walk()
	if err != nil {
		return nil, err
	}
	out := &Nodes{NodeType: class, Scripts: f.classScripts(p, class), Nodes: []NodeHit{}, Notes: f.notes()}
	sc := &scenes{p: p, f: f, parsed: map[string][]sceneNode{}}
	for _, file := range f.text {
		if hasExt(file, ".tscn") {
			out.Nodes = append(out.Nodes, sc.matches(file, class, out.Scripts)...)
		}
		if len(out.Nodes) > FindLimit {
			break
		}
	}
	out.Nodes, out.Truncated = capped(out.Nodes)
	return out, nil
}

// classScripts lists the scripts that declare class_name class.
func (f *files) classScripts(p *Project, class string) []string {
	var out []string
	for _, file := range f.text {
		if !hasExt(file, ".gd") {
			continue
		}
		if b, err := os.ReadFile(p.osPath(file)); err == nil && className(b) == class {
			out = append(out, file)
		}
	}
	return out
}

// sceneNode is a [node] section of a .tscn file.
type sceneNode struct {
	path, typ, script, instance string // instance and script as res:// paths
	line                        int
}

// scenes parses each .tscn file once, for following instances.
type scenes struct {
	p      *Project
	f      *files
	parsed map[string][]sceneNode
}

func (s *scenes) nodes(res string) []sceneNode {
	if n, ok := s.parsed[res]; ok {
		return n
	}
	var b []byte
	if s.f.known[res] { // an instance path read from a scene may lead anywhere
		b, _ = os.ReadFile(s.p.osPath(res))
	}
	s.parsed[res] = parseScene(b)
	return s.parsed[res]
}

// matches lists the nodes of scene res that are of type class or carry one
// of scripts.
func (s *scenes) matches(res, class string, scripts []string) []NodeHit {
	var hits []NodeHit
	for _, n := range s.nodes(res) {
		if typ, script := s.effective(n, 0); typ == class || slices.Contains(scripts, script) {
			hits = append(hits, NodeHit{Scene: res, Node: n.path, Line: n.line, Instance: n.instance})
		}
	}
	return hits
}

// effective is a node's type and script; an instance takes what it does not
// set itself from its scene's root. depth stops scenes that instance each
// other.
func (s *scenes) effective(n sceneNode, depth int) (typ, script string) {
	if n.instance == "" || depth > 16 {
		return n.typ, n.script
	}
	if root := s.nodes(n.instance); len(root) > 0 {
		typ, script = s.effective(root[0], depth+1)
	}
	return cmp.Or(n.typ, typ), cmp.Or(n.script, script)
}

// parseScene lists a scene's nodes, in file order (the root first).
func parseScene(data []byte) []sceneNode {
	ext := map[string]string{} // ext_resource id -> path
	var nodes []sceneNode
	inNode := false
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "[ext_resource "):
			ext[attr(line, "id")] = attr(line, "path")
		case strings.HasPrefix(line, "[node "):
			nodes, inNode = append(nodes, newNode(line, i+1, ext)), true
		case strings.HasPrefix(line, "["):
			inNode = false
		case inNode && strings.HasPrefix(line, "script = "):
			nodes[len(nodes)-1].script = ext[extID(line)]
		}
	}
	return nodes
}

// newNode reads a [node name=... type=... parent=... instance=...] header;
// its path is built from parent ("." for the root).
func newNode(line string, n int, ext map[string]string) sceneNode {
	node := sceneNode{typ: attr(line, "type"), line: n, path: "."}
	if _, inst, ok := strings.Cut(line, " instance="); ok {
		node.instance = ext[extID(inst)]
	}
	switch name, parent := attr(line, "name"), attr(line, "parent"); parent {
	case "":
	case ".":
		node.path = name
	default:
		node.path = parent + "/" + name
	}
	return node
}

// extID is the id inside ExtResource("id").
func extID(s string) string {
	_, rest, ok := strings.Cut(s, "ExtResource(")
	if !ok {
		return ""
	}
	id, _, _ := strings.Cut(rest, ")")
	return strings.Trim(id, `" `)
}
