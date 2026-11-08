package com.salterhoper.smsmcp.gateway

import android.content.ContentValues
import android.content.Context
import android.database.sqlite.SQLiteDatabase
import android.database.sqlite.SQLiteOpenHelper

/**
 * Local durable queue. Plain SQLiteOpenHelper (no Room/KSP) to keep the build
 * simple; see docs/DECISIONS.md.
 *
 * jobs:    outbound SMS from the server. message_id is the PRIMARY KEY, so a
 *          job pushed twice is stored once (idempotency hop 2).
 *          reported_state lags state until the server acks the report.
 * inbound: received SMS waiting for the server's inbound_ack.
 */
class JobStore private constructor(context: Context) :
    SQLiteOpenHelper(context, "gateway.db", null, 1) {

    data class Job(
        val id: String, val to: String, val body: String, val state: String,
        val error: String?, val reportedState: String?, val updatedAt: Long,
    )

    data class Inbound(val dedup: String, val from: String, val body: String, val receivedAt: Long)

    override fun onCreate(db: SQLiteDatabase) {
        db.execSQL(
            """CREATE TABLE jobs (
                message_id TEXT PRIMARY KEY, recipient TEXT NOT NULL, body TEXT NOT NULL,
                state TEXT NOT NULL, error TEXT, parts INTEGER NOT NULL DEFAULT 0,
                sent_parts INTEGER NOT NULL DEFAULT 0, delivered_parts INTEGER NOT NULL DEFAULT 0,
                reported_state TEXT, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL)"""
        )
        db.execSQL(
            """CREATE TABLE inbound (
                dedup TEXT PRIMARY KEY, sender TEXT NOT NULL, body TEXT NOT NULL,
                received_at INTEGER NOT NULL, acked INTEGER NOT NULL DEFAULT 0)"""
        )
    }

    override fun onUpgrade(db: SQLiteDatabase, oldVersion: Int, newVersion: Int) {}

    /** Stores a job. Returns false if it already existed (a re-push). */
    @Synchronized
    fun insertJob(id: String, to: String, body: String): Boolean {
        val now = System.currentTimeMillis()
        val v = ContentValues().apply {
            put("message_id", id); put("recipient", to); put("body", body)
            put("state", JobState.ACCEPTED); put("created_at", now); put("updated_at", now)
        }
        return writableDatabase.insertWithOnConflict("jobs", null, v, SQLiteDatabase.CONFLICT_IGNORE) != -1L
    }

    fun job(id: String): Job? = readableDatabase.rawQuery(
        "SELECT message_id, recipient, body, state, error, reported_state, updated_at FROM jobs WHERE message_id = ?",
        arrayOf(id),
    ).use { c -> if (c.moveToFirst()) readJob(c) else null }

    /** Jobs waiting to be handed to SmsManager, oldest first. */
    fun nextToSend(): Job? = readableDatabase.rawQuery(
        "SELECT message_id, recipient, body, state, error, reported_state, updated_at FROM jobs " +
            "WHERE state = ? ORDER BY created_at LIMIT 1", arrayOf(JobState.ACCEPTED),
    ).use { c -> if (c.moveToFirst()) readJob(c) else null }

    /** Jobs whose current state the server has not acknowledged yet. */
    fun unreported(): List<Job> = readableDatabase.rawQuery(
        "SELECT message_id, recipient, body, state, error, reported_state, updated_at FROM jobs " +
            "WHERE reported_state IS NULL OR reported_state != state", null,
    ).use { c -> buildList { while (c.moveToNext()) add(readJob(c)) } }

    /** Moves a job forward only (monotonic, like the server). */
    @Synchronized
    fun advance(id: String, to: String, error: String? = null): Boolean {
        val cur = job(id) ?: return false
        if (!JobState.canAdvance(cur.state, to)) return false
        val v = ContentValues().apply {
            put("state", to); put("updated_at", System.currentTimeMillis())
            if (error != null) put("error", error)
        }
        return writableDatabase.update("jobs", v, "message_id = ?", arrayOf(id)) == 1
    }

    /** Called right before SmsManager: the crash-safety write. */
    @Synchronized
    fun markSending(id: String, parts: Int): Boolean {
        val cur = job(id) ?: return false
        if (cur.state != JobState.ACCEPTED) return false
        val v = ContentValues().apply {
            put("state", JobState.SENDING); put("parts", parts); put("updated_at", System.currentTimeMillis())
        }
        return writableDatabase.update("jobs", v, "message_id = ?", arrayOf(id)) == 1
    }

    /** Records one part's sent/delivered result; returns the new job state if it changed. */
    @Synchronized
    fun partResult(id: String, delivered: Boolean, ok: Boolean, error: String?): String? {
        if (!ok) return if (advance(id, JobState.FAILED, error)) JobState.FAILED else null
        val col = if (delivered) "delivered_parts" else "sent_parts"
        writableDatabase.execSQL("UPDATE jobs SET $col = $col + 1 WHERE message_id = ?", arrayOf(id))
        val done = readableDatabase.rawQuery(
            "SELECT $col >= parts AND parts > 0 FROM jobs WHERE message_id = ?", arrayOf(id),
        ).use { c -> c.moveToFirst() && c.getInt(0) == 1 }
        if (!done) return null
        val target = if (delivered) JobState.DELIVERED else JobState.SENT
        return if (advance(id, target)) target else null
    }

    /**
     * Jobs stuck in "sending" with no carrier result for [olderThanMs] become
     * "unknown". They are never resent automatically: sending again could
     * text someone twice. A late carrier result can still move them to sent.
     */
    @Synchronized
    fun expireSending(olderThanMs: Long): Int {
        val v = ContentValues().apply {
            put("state", JobState.UNKNOWN)
            put("error", "no result from the carrier; the SMS may or may not have been sent")
            put("updated_at", System.currentTimeMillis())
        }
        return writableDatabase.update(
            "jobs", v, "state = ? AND updated_at < ?",
            arrayOf(JobState.SENDING, (System.currentTimeMillis() - olderThanMs).toString()),
        )
    }

    fun markReported(id: String, state: String) {
        writableDatabase.execSQL(
            "UPDATE jobs SET reported_state = ? WHERE message_id = ? AND state = ?", arrayOf(state, id, state),
        )
    }

    /** Stores an inbound SMS once. Returns false for a duplicate. */
    fun insertInbound(dedup: String, from: String, body: String, receivedAt: Long): Boolean {
        val v = ContentValues().apply {
            put("dedup", dedup); put("sender", from); put("body", body); put("received_at", receivedAt)
        }
        return writableDatabase.insertWithOnConflict("inbound", null, v, SQLiteDatabase.CONFLICT_IGNORE) != -1L
    }

    fun unackedInbound(): List<Inbound> = readableDatabase.rawQuery(
        "SELECT dedup, sender, body, received_at FROM inbound WHERE acked = 0 ORDER BY received_at", null,
    ).use { c -> buildList { while (c.moveToNext()) add(Inbound(c.getString(0), c.getString(1), c.getString(2), c.getLong(3))) } }

    fun ackInbound(dedup: String) {
        writableDatabase.execSQL("UPDATE inbound SET acked = 1 WHERE dedup = ?", arrayOf(dedup))
    }

    /** Drops finished, reported jobs and acked inbound older than a week. */
    fun prune() {
        val cutoff = (System.currentTimeMillis() - 7 * 24 * 3600_000L).toString()
        writableDatabase.execSQL(
            "DELETE FROM jobs WHERE reported_state = state AND state IN ('delivered','failed') AND updated_at < ?",
            arrayOf(cutoff),
        )
        writableDatabase.execSQL("DELETE FROM inbound WHERE acked = 1 AND received_at < ?", arrayOf(cutoff))
    }

    private fun readJob(c: android.database.Cursor) = Job(
        id = c.getString(0), to = c.getString(1), body = c.getString(2), state = c.getString(3),
        error = c.getString(4), reportedState = c.getString(5), updatedAt = c.getLong(6),
    )

    companion object {
        @Volatile private var instance: JobStore? = null
        fun get(context: Context): JobStore =
            instance ?: synchronized(this) { instance ?: JobStore(context.applicationContext).also { instance = it } }
    }
}
