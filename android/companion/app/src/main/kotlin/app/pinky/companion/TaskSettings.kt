package app.pinky.companion

import android.content.Context
import android.content.res.Resources
import org.json.JSONObject

/**
 * How Pinky on the phone runs a task: the model's service, model and
 * address, each kept per service, and a task's limits. The keys are in
 * Secrets. Desktop Pinky's Settings › Ask Pinky has the same choices
 * (ui/ask.go), and the core fills in the same defaults (pilot.Normalize).
 */
class TaskSettings(private val context: Context) {

    private val prefs = context.applicationContext.getSharedPreferences(PREFS, Context.MODE_PRIVATE)

    /**
     * The service's ID (ModelServices). Settings of before there were
     * services have only an API, "provider", and its address: those give
     * the service (ModelServices.serviceFor), and their keys, models and
     * addresses keep their names.
     */
    var service: String
        get() = serviceIn(get) { ModelServices.byId(context, it) != null }
        set(v) = prefs.edit().putString("service", v).apply()

    private val get: (String) -> String? = { prefs.getString(it, null) }

    /** The service of id, or the one chosen. */
    fun svc(id: String = service): ModelServices.Service =
        ModelServices.byId(context, id) ?: ModelServices.byId(context, ANTHROPIC)!!

    fun svcOrNull(id: String): ModelServices.Service? = ModelServices.byId(context, id)

    /** A service as Settings lists it: "Qwen (China)"; the custom entries by their API. */
    fun serviceLabel(res: Resources, s: ModelServices.Service = svc()): String = when {
        s.id == ModelServices.CUSTOM_ANTHROPIC -> res.getString(R.string.phone_service_anthropic)
        s.id == ModelServices.CUSTOM_OPENAI -> res.getString(R.string.phone_service_openai)
        s.region == ModelServices.REGION_CHINA -> res.getString(R.string.phone_service_region, s.name, res.getString(R.string.phone_region_china))
        s.region == ModelServices.REGION_GLOBAL -> res.getString(R.string.phone_service_region, s.name, res.getString(R.string.phone_region_global))
        else -> s.name
    }

    /** The API the service speaks: ANTHROPIC or OPENAI. */
    fun provider(id: String = service): String = svc(id).api

    /** The name the service's key is saved under in Secrets (pilot.KeyName). */
    fun keyName(id: String = service): String = svc(id).keyName

    /** Whether the service may run with no key: a server the person runs. */
    fun keyOptional(id: String = service): Boolean = svc(id).keyOptional

    /**
     * The model the person set for a service, or "". Those of before there
     * were services were kept per API: Anthropic's and the
     * Anthropic-compatible entry share one, as do OpenAI's and the
     * OpenAI-compatible one (ui/run.go, pilotModel).
     */
    fun modelSet(id: String = service): String = modelIn(get, id)

    /** The model a task uses: the one set, else the service's default; "" when it has none. */
    fun model(id: String = service): String = modelSet(id).ifEmpty { svc(id).defaultModel }

    fun setModel(id: String, v: String) {
        prefs.edit().putString("svc_model_$id", v.trim()).apply()
    }

    /**
     * The address the person set for a service, or "" for the service's
     * own. Only the custom entries had addresses before there were
     * services (ui/run.go, pilotURL).
     */
    fun baseUrl(id: String = service): String = urlIn(get, id)

    /** The address a task uses: the one set, else the service's own; "" for Anthropic's own API. */
    fun address(id: String = service): String = baseUrl(id).ifEmpty { svc(id).baseUrl }

    /** Sets the address; the service's own is kept as "", to follow it. */
    fun setBaseUrl(id: String, v: String) {
        val u = cleanUrl(v)
        prefs.edit().putString("svc_url_$id", if (u == svc(id).baseUrl) "" else u).apply()
    }

    /** Whether Settings offers models to choose from rather than a name to type. */
    fun modelList(id: String = service): Boolean = svc(id).models.isNotEmpty()

    var maxSteps: Int
        get() = prefs.getInt("max_steps", DEFAULT_STEPS).takeIf { it > 0 } ?: DEFAULT_STEPS
        set(v) = prefs.edit().putInt("max_steps", v).apply()

    var maxMinutes: Int
        get() = prefs.getInt("max_minutes", DEFAULT_MINUTES).takeIf { it > 0 } ?: DEFAULT_MINUTES
        set(v) = prefs.edit().putInt("max_minutes", v).apply()

    var maxCost: Double
        get() = prefs.getFloat("max_cost", DEFAULT_COST.toFloat()).toDouble().takeIf { it > 0 } ?: DEFAULT_COST
        set(v) = prefs.edit().putFloat("max_cost", v.toFloat()).apply()

    /** Whether tasks read the person's notes and may suggest new ones (MemoryActivity); off leaves the notes as they are. */
    var useMemory: Boolean
        get() = prefs.getBoolean("use_memory", true)
        set(v) = prefs.edit().putBoolean("use_memory", v).apply()

    /** Whether Pinky knows the model's prices, and so counts the cost limit in dollars (pilot.Config.Priced). */
    fun priced(id: String = service): Boolean = svc(id).priced(model(id))

    /** The cost limit in tokens, for a model whose prices Pinky does not know (pilot.Config.TokenLimit). */
    val tokenLimit: Long get() = (maxCost * 1_000_000).toLong()

    /** Whether the person put off the task screen's card about battery limits (Battery); Settings still has them. */
    var batteryPutOff: Boolean
        get() = prefs.getBoolean("battery_put_off", false)
        set(v) = prefs.edit().putBoolean("battery_put_off", v).apply()

    /** The model as the person reads it: its name on the service, or its ID. */
    fun modelLabel(id: String = service): String = svc(id).modelLabel(model(id))

    /** The core's start command for a task on a service, with its key ("" for none). */
    fun startRequest(task: String, key: String, id: String = service): JSONObject =
        modelRequest("start", key, id).put("task", task)

    /** The core's check command for the model of a service, with its key ("" for none). */
    fun checkRequest(key: String, id: String = service): JSONObject = modelRequest("check", key, id)

    /**
     * A command of the core that runs a task with the model's settings and
     * key (StartRequest): start, check, and the schedules' schedule_due and
     * schedule_run, whose task is the schedule's.
     */
    fun modelRequest(cmd: String, key: String, id: String = service): JSONObject = CoreClient.cmd(
        cmd,
        "service" to id,
        "key" to key,
        "provider" to provider(id),
        "model" to model(id),
        "base_url" to baseUrl(id),
        "max_steps" to maxSteps,
        "max_minutes" to maxMinutes,
        "max_cost" to maxCost,
        "no_memory" to !useMemory,
    )

    companion object {
        const val ANTHROPIC = "anthropic"
        const val OPENAI = "openai"
        const val DEFAULT_MODEL = "claude-opus-5-5"
        private const val PREFS = "task"
        private const val DEFAULT_STEPS = 100
        private const val DEFAULT_MINUTES = 20
        private const val DEFAULT_COST = 2.0

        /** The limits Settings offers (ui/ask.go). */
        val STEP_CHOICES = listOf(25, 50, 100, 200, 500)
        val MINUTE_CHOICES = listOf(5, 10, 20, 30, 60)
        val COST_CHOICES = listOf(0.5, 1.0, 2.0, 5.0, 10.0)

        // The rules of the settings kept, over get, which reads a setting
        // (null for none): pure, for the unit tests.

        /** The service chosen, or the one of settings of before there were services; known says whether this version has a service. */
        fun serviceIn(get: (String) -> String?, known: (String) -> Boolean): String {
            get("service")?.takeIf(known)?.let { return it }
            val p = if (get("provider") == OPENAI) OPENAI else ANTHROPIC
            return ModelServices.serviceFor(p, get("base_url_$p").orEmpty())
        }

        /** The model set for a service, or the one set for its API before there were services. */
        fun modelIn(get: (String) -> String?, id: String): String {
            get("svc_model_$id")?.let { return it }
            return when (id) {
                ANTHROPIC, ModelServices.CUSTOM_ANTHROPIC -> get("model_$ANTHROPIC").orEmpty()
                OPENAI, ModelServices.CUSTOM_OPENAI -> get("model_$OPENAI").orEmpty()
                else -> ""
            }
        }

        /** The address set for a service, or for a custom entry the one set for its API before there were services. */
        fun urlIn(get: (String) -> String?, id: String): String {
            get("svc_url_$id")?.let { return it }
            return when (id) {
                ModelServices.CUSTOM_ANTHROPIC -> get("base_url_$ANTHROPIC").orEmpty()
                ModelServices.CUSTOM_OPENAI -> get("base_url_$OPENAI").orEmpty()
                else -> ""
            }
        }

        /** An address as it is kept: trimmed, without a trailing slash. */
        fun cleanUrl(v: String): String = v.trim().trimEnd('/')

        /** Whether an address may be saved: empty for the default, else http or https. */
        fun urlOK(v: String): Boolean {
            val u = cleanUrl(v)
            return u.isEmpty() || u.startsWith("https://") || u.startsWith("http://")
        }
    }
}
