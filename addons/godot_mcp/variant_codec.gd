@tool
extends RefCounted
## Converts between JSON values and Godot Variants for node properties.
## Values go out in a form that comes back in unchanged: plain JSON for
## numbers, strings and booleans, res:// paths for saved resources and Godot's
## text form ("Vector2(1, 2)") for everything else.

const MAX_TEXT := 4000


static func encode(v: Variant, depth := 0) -> Variant:
	match typeof(v):
		TYPE_NIL, TYPE_BOOL, TYPE_INT, TYPE_FLOAT, TYPE_STRING:
			return v
		TYPE_STRING_NAME, TYPE_NODE_PATH:
			return str(v)
		TYPE_OBJECT:
			return _encode_object(v, depth)
		TYPE_ARRAY:
			return v.map(func(e: Variant) -> Variant: return encode(e, depth))
		TYPE_DICTIONARY:
			var out := {}
			for k in v:
				out[str(k)] = encode(v[k], depth)
			return out
	var text := var_to_str(v)
	if text.length() > MAX_TEXT:
		return text.substr(0, MAX_TEXT) + "... (%d characters, truncated)" % text.length()
	return text


static func _encode_object(v: Variant, depth: int) -> Variant:
	if not is_instance_valid(v):
		return null
	if v is InputEvent:
		return v.as_text()
	if v is Resource:
		var path: String = v.resource_path
		if not path.is_empty() and not path.contains("::"):
			return path
		return _encode_embedded(v, depth)
	if v is Node:
		return "<Node %s>" % v.name
	return "<%s>" % v.get_class()


## A resource stored inside the scene, in the form decode() accepts back:
## {"type": class, property: value, ...} with the properties that differ
## from the class defaults.
static func _encode_embedded(res: Resource, depth: int) -> Variant:
	if depth >= 3:
		return "<%s>" % res.get_class() # deeper nesting: read it with "a:b:c"
	var out := {"type": res.get_class()}
	for prop in res.get_property_list():
		if not (prop.usage & PROPERTY_USAGE_STORAGE) or prop.name.begins_with("resource_"):
			continue
		var value = res.get(prop.name)
		var default = ClassDB.class_get_property_default_value(res.get_class(), prop.name)
		if typeof(value) != typeof(default) or value != default:
			out[prop.name] = encode(value, depth + 1)
	return out


## The property-list entry of a property (type, hint, hint_string,
## class_name), following "a:b" sub-paths into resources; {} if the
## property does not exist.
static func property_info(obj: Object, path: String) -> Dictionary:
	var parts := path.split(":")
	for i in parts.size():
		var found := {}
		for info in obj.get_property_list():
			if info.name == parts[i]:
				found = info
				break
		if found.is_empty() or i == parts.size() - 1:
			return found
		var next = obj.get(parts[i])
		if not (next is Object):
			return {}
		obj = next
	return {}


## Converts a JSON value for a property. info is the property's entry from
## get_property_list(). Returns {"value": v} or {"error": message}. current
## is the property's present value, used for examples in error messages.
## Each converter below returns {} when the value is not its kind.
static func decode(value: Variant, info: Dictionary, current: Variant) -> Dictionary:
	if value == null:
		return {"value": null}
	var type: int = info.get("type", TYPE_NIL)
	if type == TYPE_OBJECT:
		return _decode_object(value, info)
	var out := _decode_simple(value, type)
	if out.is_empty():
		out = _decode_container(value, type, current)
	if out.is_empty():
		out = _decode_text(value, type)
	if out.is_empty():
		var example: String = var_to_str(current) if current != null else type_string(type) + "(...)"
		out = {"error": "expected %s, e.g. %s; got %s" % [type_string(type), example, JSON.stringify(value)]}
	return out


static func _decode_simple(value: Variant, type: int) -> Dictionary:
	match type:
		TYPE_NIL:
			return {"value": value}
		TYPE_STRING:
			return {"value": str(value)}
		TYPE_STRING_NAME:
			return {"value": StringName(str(value))}
		TYPE_NODE_PATH:
			return {"value": NodePath(str(value))}
		TYPE_BOOL:
			return _decode_bool(value)
		TYPE_INT, TYPE_FLOAT:
			return _decode_number(value, type)
		TYPE_COLOR:
			return _wrap(_color(value))
		TYPE_VECTOR2, TYPE_VECTOR2I, TYPE_VECTOR3, TYPE_VECTOR3I, TYPE_VECTOR4, TYPE_VECTOR4I:
			return _wrap(_vector(value, type))
	return {}


static func _wrap(v: Variant) -> Dictionary:
	return {} if v == null else {"value": v}


static func _decode_bool(value: Variant) -> Dictionary:
	if value is bool:
		return {"value": value}
	if value is String and value.to_lower() in ["true", "false"]:
		return {"value": value.to_lower() == "true"}
	return {}


static func _decode_number(value: Variant, type: int) -> Dictionary:
	var n = value
	if value is String:
		var valid: bool = value.is_valid_int() if type == TYPE_INT else value.is_valid_float()
		if not valid:
			return {}
		n = value.to_float()
	if not (n is float or n is int):
		return {}
	return {"value": int(n) if type == TYPE_INT else float(n)}


static func _decode_container(value: Variant, type: int, current: Variant) -> Dictionary:
	if type == TYPE_DICTIONARY and value is Dictionary:
		return {"value": value}
	if type != TYPE_ARRAY or not (value is Array):
		return {}
	if current is Array and current.is_typed():
		return {"value": Array(value, current.get_typed_builtin(), current.get_typed_class_name(), current.get_typed_script())}
	return {"value": value}


## Godot's text form, e.g. "Rect2(0, 0, 16, 16)", or a JSON array for a
## packed array.
static func _decode_text(value: Variant, type: int) -> Dictionary:
	if value is String:
		var parsed = str_to_var(value)
		if typeof(parsed) == type:
			return {"value": parsed}
	if type >= TYPE_PACKED_BYTE_ARRAY and value is Array:
		var converted = type_convert(value, type)
		if typeof(converted) == type:
			return {"value": converted}
	return {}


static func _decode_object(value: Variant, info: Dictionary) -> Dictionary:
	if value is String and value.is_empty():
		return {"value": null}
	if value is String and (value.begins_with("res://") or value.begins_with("uid://")):
		return _load_resource(value, info)
	if value is Dictionary and value.has("type"):
		return _new_resource(value, info)
	return {"error": "expected a res:// path, a new resource such as {\"type\": \"RectangleShape2D\", \"size\": [16, 16]}, or null"}


## Builds a resource from {"type": class, property: value, ...}, decoding
## each property like any other value; nested resources work the same way.
static func _new_resource(spec: Dictionary, info: Dictionary) -> Dictionary:
	var type := str(spec.type)
	if not ClassDB.class_exists(type) or not ClassDB.is_parent_class(type, "Resource"):
		return {"error": "\"%s\" is not a resource class" % type}
	if not ClassDB.can_instantiate(type):
		return {"error": "%s is abstract; use one of: %s" % [type, ", ".join(ClassDB.get_inheriters_from_class(type).slice(0, 8))]}
	var res: Resource = ClassDB.instantiate(type)
	var mismatch := _class_mismatch(res, info)
	if not mismatch.is_empty():
		return {"error": mismatch}
	for key in spec:
		var problem := "" if key == "type" else _set_field(res, str(key), spec[key])
		if not problem.is_empty():
			return {"error": "%s.%s: %s" % [type, key, problem]}
	return {"value": res}


## A new object of a class_name class, or null. Like the editor's own dialogs
## it builds the native base class and attaches the script: in the editor a
## script without @tool cannot be instantiated with new().
static func instantiate_script_class(type: String) -> Object:
	var path := _global_class_path(type)
	var script := load(path) as Script if not path.is_empty() else null
	if script == null or not ClassDB.can_instantiate(script.get_instance_base_type()):
		return null
	var obj := ClassDB.instantiate(script.get_instance_base_type()) as Object
	obj.set_script(script)
	return obj


static func _global_class_path(type: String) -> String:
	for g in ProjectSettings.get_global_class_list():
		if g["class"] == type:
			return g["path"]
	return ""


static func _set_field(obj: Object, name: String, value: Variant) -> String:
	var field := property_info(obj, name)
	if field.is_empty():
		return "no such property"
	var conv := decode(value, field, obj.get(name))
	if conv.has("error"):
		return conv.error
	obj.set(name, conv.value)
	return ""


static func _load_resource(value: String, info: Dictionary) -> Dictionary:
	var path := to_res_path(value)
	var problem := check_res_path(path)
	if problem.is_empty() and not ResourceLoader.exists(path):
		problem = "%s does not exist" % value
	if not problem.is_empty():
		return {"error": problem}
	var res := load(path)
	if res == null:
		return {"error": "%s could not be loaded; check it with script_check if it is a script" % value}
	# Godot would quietly store null for a resource of the wrong class.
	var mismatch := _class_mismatch(res, info)
	return {"error": mismatch} if not mismatch.is_empty() else {"value": res}


## Returns why res cannot go into the property described by info, or "".
## A resource-type hint lists the allowed classes, comma-separated, with a
## leading "-" for excluded ones.
static func _class_mismatch(res: Resource, info: Dictionary) -> String:
	var classes: PackedStringArray = []
	if info.get("hint", PROPERTY_HINT_NONE) == PROPERTY_HINT_RESOURCE_TYPE:
		classes = str(info.get("hint_string", "")).split(",", false)
	elif not str(info.get("class_name", "")).is_empty():
		classes = [str(info.class_name)]
	var allowed := []
	for c in classes:
		var name := c.strip_edges()
		if name.begins_with("-"):
			if _is_a(res, name.substr(1)):
				return "%s is a %s, which this property does not accept" % [res.resource_path, name.substr(1)]
		else:
			allowed.append(name)
	if allowed.is_empty():
		return ""
	for name in allowed:
		if _is_a(res, name):
			return ""
	var what := res.resource_path if not res.resource_path.is_empty() else "the new resource"
	return "needs %s, but %s is a %s" % [" or ".join(allowed), what, res.get_class()]


## Whether res is of the built-in class or class_name name.
static func _is_a(res: Resource, name: String) -> bool:
	if res.is_class(name):
		return true
	var script: Script = res.get_script()
	while script != null:
		if script.get_global_name() == name:
			return true
		script = script.get_base_script()
	return false


## Turns a uid:// or res:// reference into a clean res:// path; "\\" counts as
## a separator, as it does everywhere in Godot.
static func to_res_path(value: String) -> String:
	if value.begins_with("uid://"):
		var id := ResourceUID.text_to_id(value)
		if ResourceUID.has_id(id):
			return ResourceUID.get_id_path(id)
		return value
	return value.replace("\\", "/").simplify_path()


## Returns why a res:// path may not be used, or "" when it is fine. The
## server checks the paths it can see; these come inside property values.
## ".." can leave the project, and macOS and Windows accept a wrongly cased
## path that then breaks on Linux and in exported games.
static func check_res_path(path: String) -> String:
	if not path.begins_with("res://"):
		return "%s is not a res:// path" % path
	var parts := path.trim_prefix("res://").split("/", false)
	if parts.has(".."):
		return "%s leaves the project folder" % path
	var dir := "res://"
	for part in parts:
		var d := DirAccess.open(dir)
		if d == null:
			return ""
		d.include_hidden = true
		var names := Array(d.get_directories()) + Array(d.get_files())
		if not names.has(part):
			for n in names:
				if n.to_lower() == part.to_lower():
					return "%s has the wrong letter case: on disk it is spelled \"%s\"; Linux and exported games are case-sensitive" % [path, n]
			return "" # missing; the loader reports that
		dir = dir.path_join(part)
	return ""


static func _color(value: Variant) -> Variant:
	if value is String:
		if value.begins_with("Color("):
			var c = str_to_var(value)
			return c if c is Color else null
		if Color.html_is_valid(value):
			return Color.html(value)
		var sentinel := Color(0.123, 0.456, 0.789, 0.321)
		var named := Color.from_string(value, sentinel)
		return null if named == sentinel else named
	if value is Array and value.size() in [3, 4]:
		return Color(value[0], value[1], value[2], value[3] if value.size() == 4 else 1.0)
	if value is Dictionary and value.has_all(["r", "g", "b"]):
		return Color(value.r, value.g, value.b, value.get("a", 1.0))
	return null


static func _vector(value: Variant, type: int) -> Variant:
	var n := 2
	if type in [TYPE_VECTOR3, TYPE_VECTOR3I]:
		n = 3
	elif type in [TYPE_VECTOR4, TYPE_VECTOR4I]:
		n = 4
	var c := []
	if value is Array and value.size() == n:
		c = value
	elif value is Dictionary and value.has_all(["x", "y", "z", "w"].slice(0, n)):
		for k in ["x", "y", "z", "w"].slice(0, n):
			c.append(value[k])
	else:
		return null
	for e in c:
		if not (e is float or e is int):
			return null
	match type:
		TYPE_VECTOR2:
			return Vector2(c[0], c[1])
		TYPE_VECTOR2I:
			return Vector2i(c[0], c[1])
		TYPE_VECTOR3:
			return Vector3(c[0], c[1], c[2])
		TYPE_VECTOR3I:
			return Vector3i(c[0], c[1], c[2])
		TYPE_VECTOR4:
			return Vector4(c[0], c[1], c[2], c[3])
		TYPE_VECTOR4I:
			return Vector4i(c[0], c[1], c[2], c[3])
	return null
