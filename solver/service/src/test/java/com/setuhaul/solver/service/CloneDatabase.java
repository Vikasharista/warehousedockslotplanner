package com.setuhaul.solver.service;

import io.quarkus.test.common.QuarkusTestResourceLifecycleManager;
import io.quarkus.test.junit.QuarkusTestProfile;

import java.sql.Connection;
import java.sql.DriverManager;
import java.sql.Statement;
import java.util.List;
import java.util.Map;

/**
 * Tests that write bookings get their own copy of the seeded test database (SETUHAUL_TEST_JDBC_URL), made before the
 * application starts and dropped after, so the read-only tests keep the pristine snapshot.
 */
public class CloneDatabase implements QuarkusTestResourceLifecycleManager {

    static final String SUFFIX = "_commit_it";

    private String adminUrl;
    private String clone;

    /** Profile that points the application at the clone. */
    public static class Profile implements QuarkusTestProfile {
        @Override
        public List<TestResourceEntry> testResources() {
            return List.of(new TestResourceEntry(CloneDatabase.class));
        }
    }

    @Override
    public Map<String, String> start() {
        String url = System.getenv("SETUHAUL_TEST_JDBC_URL");
        // jdbc:postgresql://host:port/db?params
        int slash = url.indexOf('/', "jdbc:postgresql://".length());
        int q = url.indexOf('?', slash);
        String source = q < 0 ? url.substring(slash + 1) : url.substring(slash + 1, q);
        String params = q < 0 ? "" : url.substring(q);
        clone = source + SUFFIX;
        adminUrl = url.substring(0, slash + 1) + "postgres" + params;
        exec("DROP DATABASE IF EXISTS " + clone + " WITH (FORCE)",
                "CREATE DATABASE " + clone + " TEMPLATE " + source);
        return Map.of("quarkus.datasource.jdbc.url", url.substring(0, slash + 1) + clone + params);
    }

    @Override
    public void stop() {
        if (clone != null) {
            exec("DROP DATABASE IF EXISTS " + clone + " WITH (FORCE)");
        }
    }

    private void exec(String... sql) {
        try (Connection c = DriverManager.getConnection(adminUrl); Statement st = c.createStatement()) {
            for (String s : sql) {
                st.execute(s);
            }
        } catch (Exception e) {
            throw new IllegalStateException("cannot prepare the commit test database: " + e.getMessage(), e);
        }
    }
}
