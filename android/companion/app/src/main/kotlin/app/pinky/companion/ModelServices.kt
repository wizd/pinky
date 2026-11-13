package app.pinky.companion

import android.content.Context
import org.json.JSONArray
import org.json.JSONObject

/**
 * The services that run the model of Pinky on the phone, as desktop
 * Pinky offers them (internal/pilot/services.go). The list is the core's,
 * written into assets/model_services.json by `go generate ./internal/pilot`;
 * the pilot package's tests fail when the file is out of date.
 */
object ModelServices {

    /** A model a service runs: its ID, and its name when it has one. */
    data class Model(val id: String, val name: String) {
        val label: String get() = name.ifEmpty { id }
    }

    /** A service: pilot.Service. */
    data class Service(
        val id: String,
        val name: String,
        val region: String, // REGION_CHINA, REGION_GLOBAL or ""
        val api: String, // TaskSettings.ANTHROPIC or TaskSettings.OPENAI
        val baseUrl: String, // "" for Anthropic's own API and the custom entries
        val models: List<Model>, // the models Settings suggests, the default first
        val keyOptional: Boolean, // a server on a computer that may run with no key
        val custom: Boolean, // a compatible API at an address the person gives
        val local: Boolean, // a server the person runs, such as Ollama
        val priced: List<String>, // the starts of the model names whose prices Pinky knows here
    ) {
        /** The name the key of this service is saved under in Secrets (pilot.KeyName). */
        val keyName: String get() = if (custom) api else id

        /** The model a task uses when the person chose none (pilot.Config.Normalize). */
        val defaultModel: String
            get() = models.firstOrNull()?.id ?: if (api == TaskSettings.ANTHROPIC && !local) TaskSettings.DEFAULT_MODEL else ""

        /** Whether Pinky knows the prices of model here (pilot.Config.Priced). */
        fun priced(model: String): Boolean = model.isNotEmpty() && priced.any { model.startsWith(it) }

        /** A model as the person reads it: its name, or its ID. */
        fun modelLabel(model: String): String = models.firstOrNull { it.id == model }?.label ?: model
    }

    const val REGION_CHINA = "china"
    const val REGION_GLOBAL = "global"
    const val CUSTOM_ANTHROPIC = "anthropic-compatible"
    const val CUSTOM_OPENAI = "openai-compatible"
    const val OPENAI_URL = "https://api.openai.com/v1"

    @Volatile private var list: List<Service>? = null

    /** All the services, in the order Settings lists them. */
    fun all(context: Context): List<Service> = list ?: synchronized(this) {
        list ?: context.applicationContext.assets.open(FILE).bufferedReader().use { parse(it.readText()) }.also { list = it }
    }

    fun byId(context: Context, id: String): Service? = all(context).firstOrNull { it.id == id }

    /**
     * The service of settings of before there were services: the API, and
     * the address the person typed (pilot.ServiceFor). An address typed by
     * hand stays the person's, even a service's own.
     */
    fun serviceFor(provider: String, baseUrl: String): String {
        val base = TaskSettings.cleanUrl(baseUrl)
        return if (provider == TaskSettings.OPENAI) {
            if (base.isEmpty() || base == OPENAI_URL) TaskSettings.OPENAI else CUSTOM_OPENAI
        } else {
            if (base.isEmpty()) TaskSettings.ANTHROPIC else CUSTOM_ANTHROPIC
        }
    }

    /** The list in the generated file; one the app does not understand is left out. */
    fun parse(json: String): List<Service> {
        val arr = JSONObject(json).getJSONArray("services")
        val out = ArrayList<Service>(arr.length())
        for (i in 0 until arr.length()) {
            val o = arr.getJSONObject(i)
            val api = o.optString("api")
            if (api != TaskSettings.ANTHROPIC && api != TaskSettings.OPENAI) continue
            val models = o.optJSONArray("models") ?: JSONArray()
            val priced = o.optJSONArray("priced_models") ?: JSONArray()
            out += Service(
                id = o.getString("id"),
                name = o.optString("name"),
                region = o.optString("region"),
                api = api,
                baseUrl = o.optString("base_url"),
                models = (0 until models.length()).map { j ->
                    models.getJSONObject(j).let { Model(it.getString("id"), it.optString("name")) }
                },
                keyOptional = o.optBoolean("key_optional"),
                custom = o.optBoolean("custom"),
                local = o.optBoolean("local"),
                priced = (0 until priced.length()).map { priced.getString(it) },
            )
        }
        return out
    }

    private const val FILE = "model_services.json"
}
