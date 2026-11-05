extends SceneTree
# Written into <project>/.godot/godot_mcp/ by the godot-mcp server and run as
#   godot --headless --path <project> --script <this file> -- <class> <include_inherited> <filter>
# Prints the API of one built-in or class_name class as a single JSON line.
# It compiles inside the user's project, so it raises no GDScript warning.

const RESULT: String = "GODOT_MCP_RESULT "
const NOT_MEMBERS: int = PROPERTY_USAGE_CATEGORY | PROPERTY_USAGE_GROUP | PROPERTY_USAGE_SUBGROUP

var _filter: String = ""


func _initialize() -> void:
	var args: PackedStringArray = OS.get_cmdline_user_args()
	var name: String = args[0] if args.size() > 0 else ""
	if args.size() > 2:
		_filter = args[2]
	print(RESULT + JSON.stringify(_describe(name, args.size() > 1 and args[1] == "true")))
	quit()


func _describe(name: String, inherited: bool) -> Dictionary:
	if ClassDB.class_exists(name):
		return _describe_native(name, inherited)
	for c: Dictionary in ProjectSettings.get_global_class_list():
		if c["class"] != name:
			continue
		var script: Script = load(str(c["path"]))
		# A script that fails to parse still loads, but extends nothing.
		if script == null or script.get_instance_base_type().is_empty():
			return _error("SCRIPT_ERROR", "%s, which declares %s, does not load" % [c["path"], name], "run script_check on it")
		return _describe_script(name, script, inherited)
	return _error("CLASS_NOT_FOUND", "there is no class named \"%s\"" % name, _not_found_hint(name))


func _error(code: String, message: String, hint: String) -> Dictionary:
	return {"error": {"code": code, "message": message, "hint": hint}}


func _describe_native(cls: String, inherited: bool) -> Dictionary:
	var own: bool = not inherited
	var methods: Dictionary = {}
	for m: Dictionary in ClassDB.class_get_method_list(cls, own):
		methods[m["name"]] = m
	var accessors: Dictionary = {}
	var props: Array = _native_properties(cls, own, methods, accessors)
	var enums: Dictionary = {}
	var in_enum: Dictionary = {}
	for e: StringName in ClassDB.class_get_enum_list(cls, own):
		var values: Dictionary = {}
		for c: StringName in ClassDB.class_get_enum_constants(cls, e, own):
			values[c] = ClassDB.class_get_integer_constant(cls, c)
			in_enum[c] = true
		enums[e] = values
	var constants: Dictionary = {}
	for c: StringName in ClassDB.class_get_integer_constant_list(cls, own):
		if not in_enum.has(c) and _matches(c):
			constants[c] = ClassDB.class_get_integer_constant(cls, c)
	return {
		"class": cls,
		"inherits": _native_chain(ClassDB.get_parent_class(cls)),
		"properties": props,
		"methods": _methods(methods.values().filter(func(m: Dictionary) -> bool: return not accessors.has(m["name"]))),
		"signals": _signals(ClassDB.class_get_signal_list(cls, own)),
		"constants": constants,
		"enums": _enums(enums),
	}


## The properties, typed like their getters, which name the enums that the
## property list calls int. Their setters and getters go into accessors, to
## leave out of methods: the property covers them.
func _native_properties(cls: String, own: bool, methods: Dictionary, accessors: Dictionary) -> Array:
	var props: Array = []
	for p: Dictionary in ClassDB.class_get_property_list(cls, own):
		var name: StringName = p["name"]
		var getter: StringName = ClassDB.class_get_property_getter(cls, name)
		var info: Dictionary = p
		if methods.has(getter):
			info = methods[getter]["return"]
		accessors[getter] = true
		accessors[ClassDB.class_get_property_setter(cls, name)] = true
		if _is_member(p):
			props.append(_property(name, _type(info), ClassDB.class_get_property_default_value(cls, name)))
	return props


## A class_name script: the members it adds to its base script, or with
## inherited all of them plus those of its built-in base class.
# shortcut: a method the script overrides from its base script counts as the base's; reading the source would tell them apart.
func _describe_script(name: String, script: Script, inherited: bool) -> Dictionary:
	var base: Script = script.get_base_script()
	var drop: Script = base if base != null and not inherited else null
	var props: Array = []
	for p: Dictionary in _minus(script.get_script_property_list(), drop.get_script_property_list() if drop else []):
		var pname: StringName = p["name"]
		if _is_member(p):
			props.append(_property(pname, _type(p), script.get_property_default_value(pname)))
	var out: Dictionary = {
		"class": name,
		"path": script.resource_path,
		"inherits": _script_chain(base) + _native_chain(script.get_instance_base_type()),
		"properties": props,
		"methods": _methods(_minus(script.get_script_method_list(), drop.get_script_method_list() if drop else [])),
		"signals": _signals(_minus(script.get_script_signal_list(), drop.get_script_signal_list() if drop else [])),
	}
	var constants: Dictionary = _chain_constants(script) if inherited else script.get_script_constant_map()
	out.merge(_script_constants(constants, drop.get_script_constant_map() if drop else {}))
	if inherited:
		_append(out, _describe_native(script.get_instance_base_type(), true))
	return out


func _append(out: Dictionary, native: Dictionary) -> void:
	for key: String in ["properties", "methods", "signals"]:
		var list: Array = out[key]
		var more: Array = native[key]
		list.append_array(more)
	for key: String in ["constants", "enums"]:
		var map: Dictionary = out[key]
		var more: Dictionary = native[key]
		map.merge(more)


## The constants of script and its base scripts, which a script's own
## constant map leaves out; the script's own win over its bases'.
func _chain_constants(script: Script) -> Dictionary:
	var all: Dictionary = {}
	while script != null:
		all.merge(script.get_script_constant_map())
		script = script.get_base_script()
	return all


## Splits a script's constants into enums (GDScript keeps them as
## dictionaries) and the rest, leaving out those of the base script.
func _script_constants(map: Dictionary, base: Dictionary) -> Dictionary:
	var constants: Dictionary = {}
	var enums: Dictionary = {}
	for c: String in map:
		if base.has(c):
			continue
		if map[c] is Dictionary:
			enums[c] = map[c]
		elif _matches(c):
			constants[c] = _value(map[c])
	return {"constants": constants, "enums": _enums(enums)}


## The members of list whose name is not in base.
func _minus(list: Array, base: Array) -> Array:
	var names: Array = base.map(func(m: Dictionary) -> String: return m["name"])
	return list.filter(func(m: Dictionary) -> bool: return not names.has(m["name"]))


func _script_chain(script: Script) -> Array:
	var chain: Array = []
	while script != null:
		var name: String = script.get_global_name()
		chain.append(name if not name.is_empty() else script.resource_path)
		script = script.get_base_script()
	return chain


func _native_chain(cls: String) -> Array:
	var chain: Array = []
	while not cls.is_empty():
		chain.append(cls)
		cls = ClassDB.get_parent_class(cls)
	return chain


func _is_member(p: Dictionary) -> bool:
	var usage: int = p["usage"]
	var name: String = p["name"]
	return not (usage & NOT_MEMBERS) and _matches(name)


func _matches(name: String) -> bool:
	return _filter.is_empty() or name.containsn(_filter)


func _property(name: String, type: String, default: Variant) -> Dictionary:
	var out: Dictionary = {"name": name, "type": type}
	if default != null and not default is Object:
		out["default"] = _value(default)
	return out


func _methods(list: Array) -> Array:
	var out: Array = []
	for m: Dictionary in list:
		var name: String = m["name"]
		if _matches(name) and not name.begins_with("@"):
			var ret: Dictionary = m["return"]
			out.append(_signature(m) + " -> " + _type(ret, true))
	return out


func _signals(list: Array) -> Array:
	var out: Array = []
	for s: Dictionary in list:
		var name: String = s["name"]
		if _matches(name):
			out.append(_signature(s))
	return out


## The enums whose name or one of whose values matches the filter.
func _enums(enums: Dictionary) -> Dictionary:
	var out: Dictionary = {}
	for e: String in enums:
		var values: Dictionary = enums[e]
		if _matches(e) or values.keys().any(_matches):
			out[e] = values
	return out


## "name(arg: Type, opt: int = 0)", with static or virtual in front.
func _signature(m: Dictionary) -> String:
	var args: Array = m["args"]
	var defaults: Array = m.get("default_args", [])
	var first_default: int = args.size() - defaults.size()
	var shown: Array = []
	for i: int in args.size():
		var info: Dictionary = args[i]
		var arg: String = "%s: %s" % [info["name"], _type(info)]
		if i >= first_default:
			arg += " = " + var_to_str(defaults[i - first_default])
		shown.append(arg)
	var flags: int = m.get("flags", 0)
	if flags & METHOD_FLAG_VARARG:
		shown.append("...")
	var prefix: String = "static " if flags & METHOD_FLAG_STATIC else ("virtual " if flags & METHOD_FLAG_VIRTUAL else "")
	return "%s%s(%s)" % [prefix, m["name"], ", ".join(shown)]


## A readable type: the class or enum name, Array[T], or the Variant type.
func _type(info: Dictionary, is_return: bool = false) -> String:
	var type: int = info["type"]
	var cls: String = info.get("class_name", "")
	var usage: int = info.get("usage", 0)
	if type == TYPE_NIL:
		return "void" if is_return and not usage & PROPERTY_USAGE_NIL_IS_VARIANT else "Variant"
	if not cls.is_empty():
		return cls
	var hint: int = info.get("hint", PROPERTY_HINT_NONE)
	var hint_string: String = info.get("hint_string", "")
	if type in [TYPE_ARRAY, TYPE_DICTIONARY] and hint in [PROPERTY_HINT_ARRAY_TYPE, PROPERTY_HINT_DICTIONARY_TYPE, PROPERTY_HINT_TYPE_STRING] and not hint_string.is_empty():
		return "%s[%s]" % [type_string(type), ", ".join(Array(hint_string.split(";")).map(_element))]
	if type == TYPE_OBJECT and hint == PROPERTY_HINT_RESOURCE_TYPE:
		return hint_string
	return type_string(type)


## An element type from a hint string: "Node", or the exported form "24/17:Texture2D" or "2:".
func _element(hint: String) -> String:
	if not hint.contains(":"):
		return hint
	var type: int = int(hint.get_slice(":", 0).get_slice("/", 0))
	return hint.get_slice(":", 1) if type == TYPE_OBJECT else type_string(type)


## JSON keeps numbers, strings and booleans; anything else in Godot's text form.
func _value(v: Variant) -> Variant:
	if v is Resource:
		var res: Resource = v
		return res.resource_path
	if v is Object:
		return str(v)
	if typeof(v) in [TYPE_NIL, TYPE_BOOL, TYPE_INT, TYPE_FLOAT, TYPE_STRING]:
		return v
	return var_to_str(v)


func _not_found_hint(name: String) -> String:
	for t: int in TYPE_MAX:
		if type_string(t) == name:
			return "%s is a Variant type, not a class; class_info describes Object classes" % name
	var names: Array = Array(ClassDB.get_class_list())
	names.append_array(ProjectSettings.get_global_class_list().map(func(c: Dictionary) -> String: return c["class"]))
	var close: Array = names.filter(func(c: String) -> bool: return c.similarity(name) > 0.6 or c.to_lower() == name.to_lower())
	close.sort_custom(func(a: String, b: String) -> bool: return a.similarity(name) > b.similarity(name))
	if not close.is_empty():
		return "did you mean: " + ", ".join(close.slice(0, 5))
	return "use a built-in class such as Node2D or a class_name from the project's scripts; class names are case-sensitive"
