package com.setuhaul.solver.config;

import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.io.TempDir;

import java.nio.file.Files;
import java.nio.file.Path;

import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatThrownBy;

class SolverSettingsTest {

    @Test
    void defaultsMatchThePlan() {
        SolverSettings s = SolverSettings.defaults();
        assertThat(s.gateBufferMin()).isEqualTo(30);
        assertThat(s.lowConfidenceMarginMin()).isEqualTo(15);
        assertThat(s.holdExpiryMin()).isEqualTo(15);
        assertThat(s.horizonHours()).isEqualTo(12);
        assertThat(s.priorityWeight("LOW")).isEqualTo(1);
        assertThat(s.priorityWeight("NORMAL")).isEqualTo(3);
        assertThat(s.priorityWeight("HIGH")).isEqualTo(9);
        assertThat(s.priorityWeight("CRITICAL")).isEqualTo(27);
        assertThat(s.unforcedMovePenalty()).isEqualTo(240);
        assertThat(s.forcedMovePenalty()).isEqualTo(30);
        assertThat(s.pinFeasibleConfirmed()).isTrue();
    }

    @Test
    void anOverrideFileReplacesOnlyTheKeysItNames(@TempDir Path dir) throws Exception {
        Path f = dir.resolve("site.properties");
        Files.writeString(f, "gate_buffer_min=20\npriority_weight.CRITICAL=50\n");
        SolverSettings s = SolverSettings.withOverrides(f);
        assertThat(s.gateBufferMin()).isEqualTo(20);
        assertThat(s.priorityWeight("CRITICAL")).isEqualTo(50);
        assertThat(s.holdExpiryMin()).isEqualTo(15);
    }

    @Test
    void aMistypedKeyIsRejectedRatherThanIgnored(@TempDir Path dir) throws Exception {
        Path f = dir.resolve("typo.properties");
        Files.writeString(f, "gate_bufer_min=20\n");
        assertThatThrownBy(() -> SolverSettings.withOverrides(f)).hasMessageContaining("unknown setting 'gate_bufer_min'");
    }

    @Test
    void negativeBuffersAreRejected(@TempDir Path dir) throws Exception {
        Path f = dir.resolve("bad.properties");
        Files.writeString(f, "gate_buffer_min=-5\n");
        assertThatThrownBy(() -> SolverSettings.withOverrides(f)).isInstanceOf(IllegalArgumentException.class);
    }
}
