module com.philstar.stargate {
    // JavaFX
    requires javafx.controls;
    requires javafx.fxml;

    // Logging (SLF4J 2.x is fully modular)
    requires org.slf4j;

    // gRPC, Netty, Protobuf, Logback — all non-modular, bundled by jlink plugin
    // requires com.philstar.stargate.merged.module;
    requires grpc.api;
    requires grpc.stub;
    requires protobuf.java;

    // FXML reflectively instantiates controllers
    opens com.philstar.stargate             to javafx.fxml;
    opens com.philstar.stargate.controllers to javafx.fxml;

    exports com.philstar.stargate;
}
