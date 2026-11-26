package com.example.payments.customers;

import java.net.http.HttpClient;
import java.time.Duration;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.http.client.JdkClientHttpRequestFactory;
import org.springframework.web.client.RestClient;
import org.springframework.web.client.support.RestClientAdapter;
import org.springframework.web.service.invoker.HttpServiceProxyFactory;

/** Builds the orders client: a RestClient with timeouts behind the OrdersApi interface. */
@Configuration
public class OrdersClientConfig {
    @Bean
    OrdersApi ordersApi(RestClient.Builder builder,
                        @Value("${payments.orders.url}") String url,
                        @Value("${payments.orders.connect-timeout}") Duration connectTimeout,
                        @Value("${payments.orders.read-timeout}") Duration readTimeout) {
        var factory = new JdkClientHttpRequestFactory(HttpClient.newBuilder().connectTimeout(connectTimeout).build());
        factory.setReadTimeout(readTimeout);
        RestClient client = builder.baseUrl(url).requestFactory(factory).build();
        return HttpServiceProxyFactory.builderFor(RestClientAdapter.create(client)).build().createClient(OrdersApi.class);
    }
}
